package main_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cosmossdk.io/math"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/strangelove-ventures/interchaintest/v8/testreporter"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestInterchainIBC(t *testing.T) {
	kavaImageTag := os.Getenv("KAVA_TAG")
	if kavaImageTag == "" {
		kavaImageTag = "v0.26.0-rocksdb"
	}

	ctx := context.Background()

	// setup chains
	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{
		{
			Name: "kava",
			ChainConfig: ibc.ChainConfig{
				Type:           "cosmos",
				ChainID:        "kava_8888-1",
				Images:         []ibc.DockerImage{{Repository: "kava/kava", Version: kavaImageTag, UidGid: "0:0"}},
				Bin:            "kava",
				Bech32Prefix:   "kava",
				Denom:          "ukava",
				GasPrices:      "0ukava", // 0 gas price makes calculating expected balances simpler
				GasAdjustment:  1.5,
				TrustingPeriod: "168h0m0s",
				// ModifyGenesis:  cosmos.ModifyGenesis(genesis), // TODO: configure evm things for internal bridge testing
				// CoinType: "60", // might need this to sign evm txs. will need to override decimals to be 6 again.
			},
		},
		{Name: "gaia", Version: "v15.2.0", ChainConfig: ibc.ChainConfig{GasPrices: "0.0uatom"}},
		{Name: "osmosis", Version: "v24.0.1"},
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)

	kava, gaia, osmosis := chains[0].(*cosmos.CosmosChain), chains[1].(*cosmos.CosmosChain), chains[2].(*cosmos.CosmosChain)

	// setup relayer
	client, network := interchaintest.DockerSetup(t)
	r := interchaintest.NewBuiltinRelayerFactory(ibc.CosmosRly, zaptest.NewLogger(t)).
		Build(t, client, network)

	// configure interchain
	const kavaGaiaIbcPath = "kava-gaia-demo"
	const kavaOsmosisIbcPath = "kava-osmo-demo"
	ic := interchaintest.NewInterchain().
		AddChain(kava).
		AddChain(gaia).
		AddChain(osmosis).
		AddRelayer(r, "relayer").
		AddLink(interchaintest.InterchainLink{
			Chain1:  kava,
			Chain2:  gaia,
			Relayer: r,
			Path:    kavaGaiaIbcPath,
		}).
		AddLink(interchaintest.InterchainLink{
			Chain1:  kava,
			Chain2:  osmosis,
			Relayer: r,
			Path:    kavaOsmosisIbcPath,
		})

	// Log location
	f, err := interchaintest.CreateLogFile(fmt.Sprintf("%d.json", time.Now().Unix()))
	require.NoError(t, err)
	// Reporter/logs
	rep := testreporter.NewReporter(f)
	eRep := rep.RelayerExecReporter(t)

	// Build interchain
	err = ic.Build(ctx, eRep, interchaintest.InterchainBuildOptions{
		TestName:         t.Name(),
		Client:           client,
		NetworkID:        network,
		SkipPathCreation: false},
	)
	require.NoError(t, err)

	// start the relayer so we don't need to manually Flush() packets
	err = r.StartRelayer(ctx, eRep, kavaGaiaIbcPath, kavaOsmosisIbcPath)
	require.NoError(t, err)
	defer r.StopRelayer(ctx, eRep)

	// Create and Fund User Wallets
	fundAmount := math.NewInt(10_000_000)

	users := interchaintest.GetAndFundTestUsers(t, ctx, "default", fundAmount, kava, gaia, osmosis)
	kavaUser := users[0]
	gaiaUser := users[1]
	osmosisUser := users[2]

	// wait for new block to ensure initial funding complete
	height, err := kava.Height(ctx)
	require.NoError(t, err)
	h := height
	for h <= height {
		h, err = kava.Height(ctx)
		require.NoError(t, err)
	}

	// check initial balance
	kavaUserBalInitial, err := kava.GetBalance(ctx, kavaUser.FormattedAddress(), kava.Config().Denom)
	require.NoError(t, err)
	require.True(t, kavaUserBalInitial.Equal(fundAmount))

	// get ibc channel ids
	gaiaChannelInfo, err := r.GetChannels(ctx, eRep, gaia.Config().ChainID)
	require.NoError(t, err)
	osmoChannelInfo, err := r.GetChannels(ctx, eRep, osmosis.Config().ChainID)
	require.NoError(t, err)

	gaiaToKavaChannelID := gaiaChannelInfo[0].ChannelID
	kavaToGaiaChannelID := gaiaChannelInfo[0].Counterparty.ChannelID
	osmoToKavaChannelID := osmoChannelInfo[0].ChannelID
	kavaToOsmoChannelID := osmoChannelInfo[0].Counterparty.ChannelID

	// determine ibc denoms
	srcDenomTrace := transfertypes.ParseDenomTrace(transfertypes.GetPrefixedDenom("transfer", gaiaToKavaChannelID, kava.Config().Denom))
	kavaOnGaiaDenom := srcDenomTrace.IBCDenom()
	srcDenomTrace = transfertypes.ParseDenomTrace(transfertypes.GetPrefixedDenom("transfer", osmoToKavaChannelID, kava.Config().Denom))
	kavaOnOsmoDenom := srcDenomTrace.IBCDenom()

	amountToSend := math.NewInt(1_000_000)

	// IBC transfer kava to cosmoshub
	// inspired by https://github.com/strangelove-ventures/interchaintest/blob/main/examples/ibc/learn_ibc_test.go
	t.Run("ibc transfer kava -> cosmoshub", func(t *testing.T) {
		dstAddress := gaiaUser.FormattedAddress()
		transfer := ibc.WalletAmount{
			Address: dstAddress,
			Denom:   kava.Config().Denom,
			Amount:  amountToSend,
		}

		tx, err := kava.SendIBCTransfer(ctx, kavaToGaiaChannelID, kavaUser.KeyName(), transfer, ibc.TransferOptions{})
		require.NoError(t, err)
		require.NoError(t, tx.Validate())

		// manually flush packets so we don't need to wait for the relayer
		require.NoError(t, r.Flush(ctx, eRep, kavaGaiaIbcPath, kavaToGaiaChannelID))

		// verify balance deducted from kava account
		expectedBal := kavaUserBalInitial.Sub(amountToSend)
		kavaUserBalNew, err := kava.GetBalance(ctx, kavaUser.FormattedAddress(), kava.Config().Denom)
		require.NoError(t, err)
		require.True(t, kavaUserBalNew.Equal(expectedBal))

		// verify cosmoshub account received funds
		gaiaUserBalNew, err := gaia.GetBalance(ctx, gaiaUser.FormattedAddress(), kavaOnGaiaDenom)
		require.NoError(t, err)
		require.True(t, gaiaUserBalNew.Equal(amountToSend))
	})

	// use coins IBC'd to cosmoshub, send them to osmosis using pfm
	t.Run("packet forwarding middleware: cosmoshub -> kava -> osmosis", func(t *testing.T) {
		dstAddress := osmosisUser.FormattedAddress()
		transfer := ibc.WalletAmount{
			Address: "pfm", // purposefully invalid b/c we are forwarding through kava onward to osmosis!
			Denom:   kavaOnGaiaDenom,
			Amount:  amountToSend,
		}

		tx, err := gaia.SendIBCTransfer(ctx, gaiaToKavaChannelID, gaiaUser.KeyName(), transfer, ibc.TransferOptions{
			// packet forwarding middleware!
			Memo: fmt.Sprintf(`{
	"forward": {
		"receiver": "%s",
		"port": "transfer",
		"channel": "%s"
	}
}`, dstAddress, kavaToOsmoChannelID),
		})
		require.NoError(t, err)
		require.NoError(t, tx.Validate())

		require.Eventually(t, func() bool {
			// verify transfer to osmosis
			osmosisUserBalNew, err := osmosis.GetBalance(ctx, osmosisUser.FormattedAddress(), kavaOnOsmoDenom)
			require.NoError(t, err)
			return osmosisUserBalNew.Equal(amountToSend)
		}, 15*time.Second, time.Second, "osmosis never received funds")

		// verify cosmoshub account no longer has the funds
		gaiaUserBalNew, err := gaia.GetBalance(ctx, gaiaUser.FormattedAddress(), kavaOnGaiaDenom)
		require.NoError(t, err)
		require.True(t, gaiaUserBalNew.Equal(math.ZeroInt()))
	})
}
