package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ava-labs/coreth/core/vm"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/offchainreporting2/confighelper"
	"github.com/smartcontractkit/libocr/offchainreporting2/types"
	"github.com/smartcontractkit/ocr2vrf/altbn_128"
	"github.com/smartcontractkit/ocr2vrf/dkg"
	"github.com/smartcontractkit/ocr2vrf/ocr2vrf"
	ocr2vrftypes "github.com/smartcontractkit/ocr2vrf/types"
	"github.com/urfave/cli"
	"go.dedis.ch/kyber/v3"
	"go.dedis.ch/kyber/v3/group/edwards25519"
	"go.dedis.ch/kyber/v3/pairing"

	"github.com/smartcontractkit/chainlink/core/cmd"
	"github.com/smartcontractkit/chainlink/core/config"
	"github.com/smartcontractkit/chainlink/core/gethwrappers/generated/authorized_forwarder"
	dkgContract "github.com/smartcontractkit/chainlink/core/gethwrappers/ocr2vrf/generated/dkg"
	"github.com/smartcontractkit/chainlink/core/gethwrappers/ocr2vrf/generated/load_test_beacon_consumer"
	"github.com/smartcontractkit/chainlink/core/gethwrappers/ocr2vrf/generated/vrf_beacon"
	"github.com/smartcontractkit/chainlink/core/gethwrappers/ocr2vrf/generated/vrf_beacon_consumer"
	"github.com/smartcontractkit/chainlink/core/gethwrappers/ocr2vrf/generated/vrf_coordinator"
	"github.com/smartcontractkit/chainlink/core/logger"

	helpers "github.com/smartcontractkit/chainlink/core/scripts/common"
)

var suite pairing.Suite = &altbn_128.PairingSuite{}
var g1 = suite.G1()
var g2 = suite.G2()

func deployDKG(e helpers.Environment) common.Address {
	_, tx, _, err := dkgContract.DeployDKG(e.Owner, e.Ec)
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func deployVRFCoordinator(e helpers.Environment, beaconPeriodBlocks *big.Int, linkAddress string) common.Address {
	_, tx, _, err := vrf_coordinator.DeployVRFCoordinator(e.Owner, e.Ec, beaconPeriodBlocks, common.HexToAddress(linkAddress))
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func deployAuthorizedForwarder(e helpers.Environment, link common.Address, owner common.Address) common.Address {
	_, tx, _, err := authorized_forwarder.DeployAuthorizedForwarder(e.Owner, e.Ec, link, owner, common.Address{}, []byte{})
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func setAuthorizedSenders(e helpers.Environment, forwarder common.Address, senders []common.Address) {
	f, err := authorized_forwarder.NewAuthorizedForwarder(forwarder, e.Ec)
	helpers.PanicErr(err)
	tx, err := f.SetAuthorizedSenders(e.Owner, senders)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func deployVRFBeacon(e helpers.Environment, coordinatorAddress, linkAddress, dkgAddress, keyID string) common.Address {
	keyIDBytes := decodeHexTo32ByteArray(keyID)
	_, tx, _, err := vrf_beacon.DeployVRFBeacon(e.Owner, e.Ec, common.HexToAddress(coordinatorAddress), common.HexToAddress(coordinatorAddress), common.HexToAddress(dkgAddress), keyIDBytes)
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func deployVRFBeaconCoordinatorConsumer(e helpers.Environment, coordinatorAddress string, shouldFail bool, beaconPeriodBlocks *big.Int) common.Address {
	_, tx, _, err := vrf_beacon_consumer.DeployBeaconVRFConsumer(e.Owner, e.Ec, common.HexToAddress(coordinatorAddress), shouldFail, beaconPeriodBlocks)
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func deployLoadTestVRFBeaconCoordinatorConsumer(e helpers.Environment, coordinatorAddress string, shouldFail bool, beaconPeriodBlocks *big.Int) common.Address {
	_, tx, _, err := load_test_beacon_consumer.DeployLoadTestBeaconVRFConsumer(e.Owner, e.Ec, common.HexToAddress(coordinatorAddress), shouldFail, beaconPeriodBlocks)
	helpers.PanicErr(err)
	return helpers.ConfirmContractDeployed(context.Background(), e.Ec, tx, e.ChainID)
}

func addClientToDKG(e helpers.Environment, dkgAddress string, keyID string, clientAddress string) {
	keyIDBytes := decodeHexTo32ByteArray(keyID)

	dkg, err := dkgContract.NewDKG(common.HexToAddress(dkgAddress), e.Ec)
	helpers.PanicErr(err)

	tx, err := dkg.AddClient(e.Owner, keyIDBytes, common.HexToAddress(clientAddress))
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func removeClientFromDKG(e helpers.Environment, dkgAddress string, keyID string, clientAddress string) {
	keyIDBytes := decodeHexTo32ByteArray(keyID)

	dkg, err := dkgContract.NewDKG(common.HexToAddress(dkgAddress), e.Ec)
	helpers.PanicErr(err)

	tx, err := dkg.RemoveClient(e.Owner, keyIDBytes, common.HexToAddress(clientAddress))
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func setDKGConfig(e helpers.Environment, dkgAddress string, c dkgSetConfigArgs) {
	oracleIdentities := toOraclesIdentityList(
		helpers.ParseAddressSlice(c.onchainPubKeys),
		strings.Split(c.offchainPubKeys, ","),
		strings.Split(c.configPubKeys, ","),
		strings.Split(c.peerIDs, ","),
		strings.Split(c.transmitters, ","))

	ed25519Suite := edwards25519.NewBlakeSHA256Ed25519()
	var signingKeys []kyber.Point
	for _, signingKey := range strings.Split(c.dkgSigningPubKeys, ",") {
		signingKeyBytes, err := hex.DecodeString(signingKey)
		helpers.PanicErr(err)
		signingKeyPoint := ed25519Suite.Point()
		helpers.PanicErr(signingKeyPoint.UnmarshalBinary(signingKeyBytes))
		signingKeys = append(signingKeys, signingKeyPoint)
	}

	altbn128Suite := &altbn_128.PairingSuite{}
	var encryptionKeys []kyber.Point
	for _, encryptionKey := range strings.Split(c.dkgEncryptionPubKeys, ",") {
		encryptionKeyBytes, err := hex.DecodeString(encryptionKey)
		helpers.PanicErr(err)
		encryptionKeyPoint := altbn128Suite.G1().Point()
		helpers.PanicErr(encryptionKeyPoint.UnmarshalBinary(encryptionKeyBytes))
		encryptionKeys = append(encryptionKeys, encryptionKeyPoint)
	}

	keyIDBytes := decodeHexTo32ByteArray(c.keyID)

	offchainConfig, err := dkg.OffchainConfig(encryptionKeys, signingKeys, &altbn_128.G1{}, &ocr2vrftypes.PairingTranslation{
		Suite: &altbn_128.PairingSuite{},
	})
	helpers.PanicErr(err)
	onchainConfig, err := dkg.OnchainConfig(dkg.KeyID(keyIDBytes))
	helpers.PanicErr(err)

	fmt.Println("dkg offchain config:", hex.EncodeToString(offchainConfig))
	fmt.Println("dkg onchain config:", hex.EncodeToString(onchainConfig))

	_, _, f, onchainConfig, offchainConfigVersion, offchainConfig, err := confighelper.ContractSetConfigArgsForTests(
		c.deltaProgress,
		c.deltaResend,
		c.deltaRound,
		c.deltaGrace,
		c.deltaStage,
		c.maxRounds,
		helpers.ParseIntSlice(c.schedule),
		oracleIdentities,
		offchainConfig,
		c.maxDurationQuery,
		c.maxDurationObservation,
		c.maxDurationReport,
		c.maxDurationAccept,
		c.maxDurationTransmit,
		int(c.f),
		onchainConfig)

	helpers.PanicErr(err)

	dkg := newDKG(common.HexToAddress(dkgAddress), e.Ec)

	tx, err := dkg.SetConfig(e.Owner, helpers.ParseAddressSlice(c.onchainPubKeys), helpers.ParseAddressSlice(c.transmitters), f, onchainConfig, offchainConfigVersion, offchainConfig)
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func setVRFBeaconConfig(e helpers.Environment, vrfBeaconAddr string, c vrfBeaconSetConfigArgs) {
	oracleIdentities := toOraclesIdentityList(
		helpers.ParseAddressSlice(c.onchainPubKeys),
		strings.Split(c.offchainPubKeys, ","),
		strings.Split(c.configPubKeys, ","),
		strings.Split(c.peerIDs, ","),
		strings.Split(c.transmitters, ","))

	confDelays := make(map[uint32]struct{})
	for _, c := range strings.Split(c.confDelays, ",") {
		confDelay, err := strconv.ParseUint(c, 0, 32)
		helpers.PanicErr(err)
		confDelays[uint32(confDelay)] = struct{}{}
	}

	onchainConfig := ocr2vrf.OnchainConfig(confDelays)

	_, _, f, onchainConfig, offchainConfigVersion, offchainConfig, err := confighelper.ContractSetConfigArgsForTests(
		c.deltaProgress,
		c.deltaResend,
		c.deltaRound,
		c.deltaGrace,
		c.deltaStage,
		c.maxRounds,
		helpers.ParseIntSlice(c.schedule),
		oracleIdentities,
		nil, // off-chain config
		c.maxDurationQuery,
		c.maxDurationObservation,
		c.maxDurationReport,
		c.maxDurationAccept,
		c.maxDurationTransmit,
		int(c.f),
		onchainConfig)

	helpers.PanicErr(err)

	beacon := newVRFBeacon(common.HexToAddress(vrfBeaconAddr), e.Ec)

	tx, err := beacon.SetConfig(e.Owner, helpers.ParseAddressSlice(c.onchainPubKeys), helpers.ParseAddressSlice(c.transmitters), f, onchainConfig, offchainConfigVersion, offchainConfig)
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func setProducer(e helpers.Environment, vrfCoordinatorAddr, vrfBeaconAddr string) {
	coordinator := newVRFCoordinator(common.HexToAddress(vrfCoordinatorAddr), e.Ec)

	tx, err := coordinator.SetProducer(e.Owner, common.HexToAddress(vrfBeaconAddr))
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func toOraclesIdentityList(onchainPubKeys []common.Address, offchainPubKeys, configPubKeys, peerIDs, transmitters []string) []confighelper.OracleIdentityExtra {
	offchainPubKeysBytes := []types.OffchainPublicKey{}
	for _, pkHex := range offchainPubKeys {
		pkBytes, err := hex.DecodeString(pkHex)
		helpers.PanicErr(err)
		pkBytesFixed := [ed25519.PublicKeySize]byte{}
		n := copy(pkBytesFixed[:], pkBytes)
		if n != ed25519.PublicKeySize {
			panic("wrong num elements copied")
		}

		offchainPubKeysBytes = append(offchainPubKeysBytes, types.OffchainPublicKey(pkBytesFixed))
	}

	configPubKeysBytes := []types.ConfigEncryptionPublicKey{}
	for _, pkHex := range configPubKeys {
		pkBytes, err := hex.DecodeString(pkHex)
		helpers.PanicErr(err)

		pkBytesFixed := [ed25519.PublicKeySize]byte{}
		n := copy(pkBytesFixed[:], pkBytes)
		if n != ed25519.PublicKeySize {
			panic("wrong num elements copied")
		}

		configPubKeysBytes = append(configPubKeysBytes, types.ConfigEncryptionPublicKey(pkBytesFixed))
	}

	o := []confighelper.OracleIdentityExtra{}
	for index := range configPubKeys {
		o = append(o, confighelper.OracleIdentityExtra{
			OracleIdentity: confighelper.OracleIdentity{
				OnchainPublicKey:  onchainPubKeys[index][:],
				OffchainPublicKey: offchainPubKeysBytes[index],
				PeerID:            peerIDs[index],
				TransmitAccount:   types.Account(transmitters[index]),
			},
			ConfigEncryptionPublicKey: configPubKeysBytes[index],
		})
	}
	return o
}

func requestRandomness(e helpers.Environment, coordinatorAddress string, numWords uint16, subID uint64, confDelay *big.Int) {
	coordinator := newVRFCoordinator(common.HexToAddress(coordinatorAddress), e.Ec)

	tx, err := coordinator.RequestRandomness(e.Owner, numWords, subID, confDelay)
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func redeemRandomness(e helpers.Environment, coordinatorAddress string, requestID *big.Int) {
	coordinator := newVRFCoordinator(common.HexToAddress(coordinatorAddress), e.Ec)

	tx, err := coordinator.RedeemRandomness(e.Owner, requestID)
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)
}

func requestRandomnessFromConsumer(e helpers.Environment, consumerAddress string, numWords uint16, subID uint64, confDelay *big.Int) *big.Int {
	consumer := newVRFBeaconCoordinatorConsumer(common.HexToAddress(consumerAddress), e.Ec)

	tx, err := consumer.TestRequestRandomness(e.Owner, numWords, subID, confDelay)
	helpers.PanicErr(err)
	receipt := helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)

	periodBlocks, err := consumer.IBeaconPeriodBlocks(nil)
	helpers.PanicErr(err)

	blockNumber := receipt.BlockNumber
	periodOffset := new(big.Int).Mod(blockNumber, periodBlocks)
	nextBeaconOutputHeight := new(big.Int).Sub(new(big.Int).Add(blockNumber, periodBlocks), periodOffset)

	fmt.Println("nextBeaconOutputHeight: ", nextBeaconOutputHeight)

	requestID, err := consumer.SRequestsIDs(nil, nextBeaconOutputHeight, confDelay)
	helpers.PanicErr(err)
	fmt.Println("requestID: ", requestID)

	return requestID
}

func readRandomness(
	e helpers.Environment,
	consumerAddress string,
	requestID *big.Int,
	numWords int) {
	consumer := newVRFBeaconCoordinatorConsumer(common.HexToAddress(consumerAddress), e.Ec)
	for i := 0; i < numWords; i++ {
		r, err := consumer.SReceivedRandomnessByRequestID(nil, requestID, big.NewInt(int64(i)))
		helpers.PanicErr(err)
		fmt.Println("random word", i, ":", r.String())
	}
}

func requestRandomnessCallback(
	e helpers.Environment,
	consumerAddress string,
	numWords uint16,
	subID uint64,
	confDelay *big.Int,
	callbackGasLimit uint32,
	args []byte,
) (requestID *big.Int) {
	consumer := newVRFBeaconCoordinatorConsumer(common.HexToAddress(consumerAddress), e.Ec)

	tx, err := consumer.TestRequestRandomnessFulfillment(e.Owner, subID, numWords, confDelay, callbackGasLimit, args)
	helpers.PanicErr(err)
	receipt := helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID, "TestRequestRandomnessFulfillment")

	periodBlocks, err := consumer.IBeaconPeriodBlocks(nil)
	helpers.PanicErr(err)

	blockNumber := receipt.BlockNumber
	periodOffset := new(big.Int).Mod(blockNumber, periodBlocks)
	nextBeaconOutputHeight := new(big.Int).Sub(new(big.Int).Add(blockNumber, periodBlocks), periodOffset)

	fmt.Println("nextBeaconOutputHeight: ", nextBeaconOutputHeight)

	requestID, err = consumer.SRequestsIDs(nil, nextBeaconOutputHeight, confDelay)
	helpers.PanicErr(err)
	fmt.Println("requestID: ", requestID)

	return requestID
}

func redeemRandomnessFromConsumer(e helpers.Environment, consumerAddress string, requestID *big.Int, numWords int64) {
	consumer := newVRFBeaconCoordinatorConsumer(common.HexToAddress(consumerAddress), e.Ec)

	tx, err := consumer.TestRedeemRandomness(e.Owner, requestID)
	helpers.PanicErr(err)
	helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID)

	printRandomnessFromConsumer(consumer, requestID, numWords)
}

func printRandomnessFromConsumer(consumer *vrf_beacon_consumer.BeaconVRFConsumer, requestID *big.Int, numWords int64) {
	for i := int64(0); i < numWords; i++ {
		randomness, err := consumer.SReceivedRandomnessByRequestID(nil, requestID, big.NewInt(0))
		helpers.PanicErr(err)
		fmt.Println("random words index", i, ":", randomness.String())
	}
}

func newVRFCoordinator(addr common.Address, client *ethclient.Client) *vrf_coordinator.VRFCoordinator {
	coordinator, err := vrf_coordinator.NewVRFCoordinator(addr, client)
	helpers.PanicErr(err)
	return coordinator
}

func newDKG(addr common.Address, client *ethclient.Client) *dkgContract.DKG {
	dkg, err := dkgContract.NewDKG(addr, client)
	helpers.PanicErr(err)
	return dkg
}

func newVRFBeaconCoordinatorConsumer(addr common.Address, client *ethclient.Client) *vrf_beacon_consumer.BeaconVRFConsumer {
	consumer, err := vrf_beacon_consumer.NewBeaconVRFConsumer(addr, client)
	helpers.PanicErr(err)
	return consumer
}

func newLoadTestVRFBeaconCoordinatorConsumer(addr common.Address, client *ethclient.Client) *load_test_beacon_consumer.LoadTestBeaconVRFConsumer {
	consumer, err := load_test_beacon_consumer.NewLoadTestBeaconVRFConsumer(addr, client)
	helpers.PanicErr(err)
	return consumer
}

func newVRFBeacon(addr common.Address, client *ethclient.Client) *vrf_beacon.VRFBeacon {
	beacon, err := vrf_beacon.NewVRFBeacon(addr, client)
	helpers.PanicErr(err)
	return beacon
}

func decodeHexTo32ByteArray(val string) (byteArray [32]byte) {
	decoded, err := hex.DecodeString(val)
	helpers.PanicErr(err)
	if len(decoded) != 32 {
		panic(fmt.Sprintf("expected value to be 32 bytes but received %d bytes", len(decoded)))
	}
	copy(byteArray[:], decoded)
	return
}

func setupOCR2VRFNodeFromClient(client *cmd.Client, context *cli.Context) *cmd.SetupOCR2VRFNodePayload {
	payload, err := client.ConfigureOCR2VRFNode(context)
	helpers.PanicErr(err)

	return payload
}

func configureEnvironmentVariables(useForwarder bool) {
	helpers.PanicErr(os.Setenv("ETH_USE_FORWARDERS", fmt.Sprintf("%t", useForwarder)))
	helpers.PanicErr(os.Setenv("FEATURE_OFFCHAIN_REPORTING2", "true"))
	helpers.PanicErr(os.Setenv("SKIP_DATABASE_PASSWORD_COMPLEXITY_CHECK", "true"))
}

func resetDatabase(client *cmd.Client, context *cli.Context, index int, databasePrefix string, databaseSuffixes string) {
	helpers.PanicErr(os.Setenv("DATABASE_URL", fmt.Sprintf("%s-%d?%s", databasePrefix, index, databaseSuffixes)))
	helpers.PanicErr(client.ResetDatabase(context))
}

func newSetupClient() *cmd.Client {
	lggr, closeLggr := logger.NewLogger()
	cfg := config.NewGeneralConfig(lggr)

	prompter := cmd.NewTerminalPrompter()
	return &cmd.Client{
		Renderer:                       cmd.RendererTable{Writer: os.Stdout},
		Config:                         cfg,
		Logger:                         lggr,
		CloseLogger:                    closeLggr,
		AppFactory:                     cmd.ChainlinkAppFactory{},
		KeyStoreAuthenticator:          cmd.TerminalKeyStoreAuthenticator{Prompter: prompter},
		FallbackAPIInitializer:         cmd.NewPromptingAPIInitializer(prompter, lggr),
		Runner:                         cmd.ChainlinkRunner{},
		PromptingSessionRequestBuilder: cmd.NewPromptingSessionRequestBuilder(prompter),
		ChangePasswordPrompter:         cmd.NewChangePasswordPrompter(),
		PasswordPrompter:               cmd.NewPasswordPrompter(),
	}
}

func requestRandomnessCallbackBatch(
	e helpers.Environment,
	consumerAddress string,
	numWords uint16,
	subID uint64,
	confDelay *big.Int,
	callbackGasLimit uint32,
	args []byte,
	batchSize *big.Int,
) (requestID *big.Int) {
	consumer := newLoadTestVRFBeaconCoordinatorConsumer(common.HexToAddress(consumerAddress), e.Ec)

	tx, err := consumer.TestRequestRandomnessFulfillmentBatch(e.Owner, subID, numWords, confDelay, callbackGasLimit, args, batchSize)
	helpers.PanicErr(err)
	receipt := helpers.ConfirmTXMined(context.Background(), e.Ec, tx, e.ChainID, "TestRequestRandomnessFulfillment")

	periodBlocks, err := consumer.IBeaconPeriodBlocks(nil)
	helpers.PanicErr(err)

	blockNumber := receipt.BlockNumber
	periodOffset := new(big.Int).Mod(blockNumber, periodBlocks)
	nextBeaconOutputHeight := new(big.Int).Sub(new(big.Int).Add(blockNumber, periodBlocks), periodOffset)

	fmt.Println("nextBeaconOutputHeight: ", nextBeaconOutputHeight)

	requestID, err = consumer.SRequestsIDs(nil, nextBeaconOutputHeight, confDelay)
	helpers.PanicErr(err)
	fmt.Println("requestID: ", requestID)

	return requestID
}

func verifyBeaconRandomness(e helpers.Environment, dkgAddress, beaconAddress, keyID string, height, confDelay uint64) {
	dkg, err := dkgContract.NewDKG(common.HexToAddress(dkgAddress), e.Ec)
	helpers.PanicErr(err)

	dkgConfig, err := dkg.LatestConfigDetails(nil)
	helpers.PanicErr(err)

	keyIDBytes := decodeHexTo32ByteArray(keyID)

	// Get public key from DKG
	keyData, err := dkg.GetKey(nil, keyIDBytes, dkgConfig.ConfigDigest)
	helpers.PanicErr(err)
	kg := &altbn_128.G2{}
	pk := kg.Point()
	err = pk.UnmarshalBinary(keyData.PublicKey)
	helpers.PanicErr(err)

	// Create VRF message based on block height and confirmation delay, then hash-to-curve
	blockNumber := big.NewInt(0).SetUint64(height)
	block, err := e.Ec.BlockByNumber(context.Background(), blockNumber)
	helpers.PanicErr(err)
	b := ocr2vrftypes.Block{
		Height:            height,
		ConfirmationDelay: uint32(confDelay),
		Hash:              block.Hash(),
	}
	h := b.VRFHash(dkgConfig.ConfigDigest, pk)
	hpoint := altbn_128.NewHashProof(h).HashPoint
	fmt.Println("h", h)
	fmt.Println("hpoint", hpoint)

	// Get VRF signature (VRF seed) from VRF Beacon contract
	latestBlock, err := e.Ec.HeaderByNumber(context.Background(), nil)
	helpers.PanicErr(err)
	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(0).SetUint64(height),
		ToBlock:   latestBlock.Number,
		Addresses: []common.Address{
			common.HexToAddress(beaconAddress),
		},
		Topics: [][]common.Hash{
			[]common.Hash{
				vrf_beacon.VRFBeaconNewTransmission{}.Topic(),
			},
		},
	}
	logs, err := e.Ec.FilterLogs(context.Background(), query)
	helpers.PanicErr(err)

	beacon := newVRFBeacon(common.HexToAddress(beaconAddress), e.Ec)

	var vrfOutput [2]*big.Int
	for _, log := range logs {
		fmt.Println(log)
		unpacked, err := beacon.ParseLog(log)
		helpers.PanicErr(err)

		t, ok := unpacked.(*vrf_beacon.VRFBeaconNewTransmission)
		if !ok {
			helpers.PanicErr(errors.New("failed to convert log to VRFBeaconNewTransmission"))
		}
		fmt.Println(t)
		for _, o := range t.OutputsServed {
			if o.ConfirmationDelay.Uint64() == confDelay && o.Height == height {
				vrfOutput = o.VrfOutput.VrfOutput.P
				break
			}
		}
	}

	if vrfOutput[0].Uint64() == 0 || vrfOutput[1].Uint64() == 0 {
		helpers.PanicErr(errors.New("VRFOutput seed is empty"))
	}
	// contract(0x8, -b_x, -b_y, pk_x, pk_y, p_x, p_y, g2_x, g2_y) == 1
	// b := hashToCurve(m)
	g2Base := g2.Point().Base()

	hb, err := hpoint.MarshalBinary()
	helpers.PanicErr(err)
	if len(hb) != 64 {
		panic("wrong length of hash to curve point")
	}
	input := make([]byte, 384)
	copy(input[:64], hb) // hb must be 64 bytes (32 for each ordinate)

	pkb, err := pk.MarshalBinary()
	helpers.PanicErr(err)
	copy(input[64:192], pkb) // pubkey is 128 bytes (64 for each ordinate, each ordinate a point itself)

	// output is 64 bytes, 32 for each ordinate (point in G1)
	copy(input[192:224], vrfOutput[0].Bytes())
	copy(input[224:256], vrfOutput[1].Bytes())

	// 128 bytes for the g2 generator, 64 for each ordinate
	g2b, err := g2Base.MarshalBinary()
	copy(input[256:384], g2b)

	contract := vm.PrecompiledContractsByzantium[common.HexToAddress("0x8")]
	fmt.Println("input:", input)
	res, _, err := contract.Run(nil, nil, common.Address{}, input, 10000000000000000, false)
	helpers.PanicErr(err)
	fmt.Println("verification output", res)
}
