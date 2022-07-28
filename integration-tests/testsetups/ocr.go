// Package testsetups compresses common test setups and more complicated setups like performance and chaos tests.
package testsetups

//revive:disable:dot-imports
import (
	"context"
	"math/big"
	"time"

	geth "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog/log"

	"github.com/smartcontractkit/chainlink-env/environment"
	"github.com/smartcontractkit/chainlink-testing-framework/blockchain"
	ctfClient "github.com/smartcontractkit/chainlink-testing-framework/client"
	"github.com/smartcontractkit/chainlink-testing-framework/contracts/ethereum"
	reportModel "github.com/smartcontractkit/chainlink-testing-framework/testreporters"
	"github.com/smartcontractkit/chainlink-testing-framework/testsetups"
	"github.com/smartcontractkit/chainlink/integration-tests/actions"
	"github.com/smartcontractkit/chainlink/integration-tests/client"
	"github.com/smartcontractkit/chainlink/integration-tests/contracts"
	"github.com/smartcontractkit/chainlink/integration-tests/testreporters"
)

// OCRSoakTest defines a typical OCR soak test
type OCRSoakTest struct {
	Inputs       *OCRSoakTestInputs
	TestReporter testreporters.OCRSoakTestReporter

	testEnvironment *environment.Environment
	chainlinkNodes  []client.Chainlink
	chainClient     blockchain.EVMClient
	mockServer      *ctfClient.MockserverClient

	ocrInstances   []contracts.OffChainAggregator
	ocrInstanceMap map[string]contracts.OffChainAggregator
}

// OCRSoakTestInputs define required inputs to run an OCR soak test
type OCRSoakTestInputs struct {
	BlockchainClient     blockchain.EVMClient // Client for the test to connect to the blockchain with
	TestDuration         time.Duration        // How long to run the test for (assuming things pass)
	NumberOfContracts    int                  // Number of OCR contracts to launch
	ChainlinkNodeFunding *big.Float           // Amount of ETH to fund each chainlink node with
	ExpectedRoundTime    time.Duration        // How long each round is expected to take
	TimeBetweenRounds    time.Duration        // How long to wait after a completed round to start a new one, set 0 for instant
	StartingAdapterValue int                  // Which value to start the test with on the mock adapter
}

// NewOCRSoakTest creates a new OCR soak test to setup and run
func NewOCRSoakTest(inputs *OCRSoakTestInputs) *OCRSoakTest {
	if inputs.StartingAdapterValue == 0 {
		inputs.StartingAdapterValue = 5
	}
	return &OCRSoakTest{
		Inputs: inputs,
		TestReporter: testreporters.OCRSoakTestReporter{
			ContractReports:       make(map[string]*testreporters.OCRReport),
			ExpectedRoundDuration: inputs.ExpectedRoundTime,
		},
		ocrInstanceMap: make(map[string]contracts.OffChainAggregator),
	}
}

// Setup sets up the test environment, deploying contracts and funding chainlink nodes
func (t *OCRSoakTest) Setup(env *environment.Environment) {
	t.ensureInputValues()
	t.testEnvironment = env
	var err error

	// Make connections to soak test resources
	contractDeployer, err := contracts.NewContractDeployer(t.chainClient)
	Expect(err).ShouldNot(HaveOccurred(), "Deploying contracts shouldn't fail")
	t.chainlinkNodes, err = client.ConnectChainlinkNodes(env)
	Expect(err).ShouldNot(HaveOccurred(), "Connecting to chainlink nodes shouldn't fail")
	t.mockServer, err = ctfClient.ConnectMockServer(env)
	Expect(err).ShouldNot(HaveOccurred(), "Creating mockserver clients shouldn't fail")
	t.chainClient.ParallelTransactions(true)

	// Deploy LINK
	linkTokenContract, err := contractDeployer.DeployLinkTokenContract()
	Expect(err).ShouldNot(HaveOccurred(), "Deploying Link Token Contract shouldn't fail")

	// Fund Chainlink nodes, excluding the bootstrap node
	err = actions.FundChainlinkNodes(t.chainlinkNodes[1:], t.chainClient, t.Inputs.ChainlinkNodeFunding)
	Expect(err).ShouldNot(HaveOccurred(), "Error funding Chainlink nodes")

	t.ocrInstances = actions.DeployOCRContracts(
		t.Inputs.NumberOfContracts,
		linkTokenContract,
		contractDeployer,
		t.chainlinkNodes,
		t.chainClient,
	)
	err = t.chainClient.WaitForEvents()
	Expect(err).ShouldNot(HaveOccurred(), "Error waiting for OCR contracts to be deployed")
	for _, ocrInstance := range t.ocrInstances {
		t.ocrInstanceMap[ocrInstance.Address()] = ocrInstance
		t.TestReporter.ContractReports[ocrInstance.Address()] = &testreporters.OCRReport{
			ContractAddress: ocrInstance.Address(),
			UpdatedAnswers: []*testreporters.OCRAnswerUpdated{
				{
					ContractAddress: ocrInstance.Address(),
					ExpectedRoundId: 1,
					ExpectedAnswer:  t.Inputs.StartingAdapterValue,
				},
			},
			ExpectedRoundDuration: t.Inputs.ExpectedRoundTime,
		}
	}
	log.Info().Msg("OCR Soak Test Setup Complete")
}

// Run starts the OCR soak test
func (t *OCRSoakTest) Run() {
	// Set initial value and create jobs
	By("Setting adapter responses",
		actions.SetAllAdapterResponsesToTheSameValue(t.Inputs.StartingAdapterValue, t.ocrInstances, t.chainlinkNodes, t.mockServer))
	By("Creating OCR jobs", actions.CreateOCRJobs(t.ocrInstances, t.chainlinkNodes, t.mockServer))

	log.Info().
		Str("Test Duration", t.Inputs.TestDuration.Truncate(time.Second).String()).
		Int("Number of OCR Contracts", len(t.ocrInstances)).
		Msg("Starting OCR Soak Test")

	testDuration := time.NewTimer(t.Inputs.TestDuration)

	stopTestChannel := make(chan struct{}, 1)
	testsetups.StartRemoteControlServer("OCR Soak Test", stopTestChannel)

	// Test Loop
	lastAdapterValue, currentAdapterValue := t.Inputs.StartingAdapterValue, t.Inputs.StartingAdapterValue*25
	newRoundTrigger := time.NewTimer(0)
	answerUpdated := make(chan *ethereum.OffchainAggregatorAnswerUpdated)
	t.subscribeOCREvents(answerUpdated)
	remainingExpectedAnswers := len(t.ocrInstances)
	log.Info().Msg("Running Main OCR Soak Test Loop")
	for {
		select {
		case <-stopTestChannel:
			t.TestReporter.UnexpectedShutdown = true
			log.Warn().Msg("Received shut down signal. Soak test stopping early")
			return
		case <-testDuration.C:
			if remainingExpectedAnswers > 0 {
				testDuration = time.NewTimer(time.Millisecond * 5) // Waiting for the last round to complete
			} else {
				log.Info().Msg("Soak test complete")
				return
			}
		case answer := <-answerUpdated:
			if t.processNewAnswer(answer) {
				remainingExpectedAnswers--
			}
			if remainingExpectedAnswers <= 0 {
				log.Info().
					Str("Wait time", t.Inputs.TimeBetweenRounds.String()).
					Msg("All Expected Answers Reported. Waiting to Start a New Round")
				remainingExpectedAnswers = len(t.ocrInstances)
				newRoundTrigger = time.NewTimer(t.Inputs.TimeBetweenRounds)
			}
		case <-newRoundTrigger.C:
			// Before round starts, swap values
			lastAdapterValue, currentAdapterValue = currentAdapterValue, lastAdapterValue
			t.triggerNewRound(currentAdapterValue)
		}
	}
}

// ***************
// *** Helpers ***
// ***************

// Networks returns the networks that the test is running on
func (t *OCRSoakTest) TearDownVals() (*environment.Environment, []client.Chainlink, reportModel.TestReporter, blockchain.EVMClient) {
	return t.testEnvironment, t.chainlinkNodes, &t.TestReporter, t.chainClient
}

// subscribeToAnswerUpdatedEvent subscribes to the event log for AnswerUpdated event and
// verifies if the answer is matching with the expected value
func (t *OCRSoakTest) subscribeOCREvents(
	answerUpdated chan *ethereum.OffchainAggregatorAnswerUpdated,
) {
	contractABI, err := ethereum.OffchainAggregatorMetaData.GetAbi()
	Expect(err).ShouldNot(HaveOccurred(), "Getting contract abi for OCR shouldn't fail")
	query := geth.FilterQuery{
		Addresses: []common.Address{},
	}
	for i := 0; i < len(t.ocrInstances); i++ {
		query.Addresses = append(query.Addresses, common.HexToAddress(t.ocrInstances[i].Address()))
	}
	eventLogs := make(chan types.Log)
	sub, err := t.chainClient.SubscribeFilterLogs(context.Background(), query, eventLogs)
	if err != nil {
		Expect(err).ShouldNot(HaveOccurred(), "Subscribing to contract event log for OCR instance shouldn't fail")
	}
	go func() {
		defer GinkgoRecover()

		for {
			select {
			case err := <-sub.Err():
				Expect(err).ShouldNot(HaveOccurred(), "Retrieving event subscription log in OCR instances shouldn't fail")
			case vLog := <-eventLogs:
				// The first topic is the hashed event signature
				go t.processNewEvent(sub, answerUpdated, vLog, t.ocrInstances[0], contractABI)
			}
		}
	}()
}

// processes new answer data when receiving an AnswerUpdatedEvent. Also marks if there are any anomalies between
// expected and seen results
func (t *OCRSoakTest) processNewAnswer(newAnswer *ethereum.OffchainAggregatorAnswerUpdated) bool {
	// Updated Info
	answerAddress := newAnswer.Raw.Address.Hex()
	answers, tracked := t.TestReporter.ContractReports[answerAddress]
	if !tracked {
		log.Error().Str("Untracked Address", answerAddress).Msg("Received AnswerUpdated event on an untracked OCR instance")
		return false
	}
	currentAnswer := answers.UpdatedAnswers[len(answers.UpdatedAnswers)-1]
	currentAnswer.UpdatedTime = time.Now()
	currentAnswer.UpdatedRoundId = newAnswer.RoundId.Uint64()
	currentAnswer.UpdatedBlockNum = newAnswer.Raw.BlockNumber
	currentAnswer.UpdatedAnswer = int(newAnswer.Current.Int64())

	// On-Chain Info
	updatedOCRInstance := t.ocrInstanceMap[answerAddress]
	onChainData, err := updatedOCRInstance.GetLatestRound(context.Background())
	Expect(err).ShouldNot(HaveOccurred(), "Error retrieving on-chain data for '%s' at round '%d'", answerAddress, currentAnswer.UpdatedRoundId)
	currentAnswer.OnChainAnswer = int(onChainData.Answer.Int64())
	currentAnswer.OnChainRoundId = onChainData.RoundId.Uint64()

	if !currentAnswer.ExpectedUpdate { // If unexpected, backfill some data from on-chain
		log.Warn().Str("Address", answerAddress).Msg("OCR Update was unexpected")
		currentAnswer.StartingBlockNum = onChainData.StartedAt.Uint64()
		currentAnswer.StartingTime = time.Now()
	}

	currentAnswer.RoundDuration = time.Since(currentAnswer.StartingTime)
	currentAnswer.BlockDuration = currentAnswer.UpdatedBlockNum - currentAnswer.StartingBlockNum
	log.Info().
		Uint64("Updated Round ID", currentAnswer.UpdatedRoundId).
		Uint64("Expected Round ID", currentAnswer.ExpectedRoundId).
		Int("Updated Answer", currentAnswer.UpdatedAnswer).
		Int("Expected Answer", currentAnswer.ExpectedAnswer).
		Str("Address", answerAddress).
		Uint64("Block Number", currentAnswer.UpdatedBlockNum).
		Str("Block Hash", newAnswer.Raw.BlockHash.Hex()).
		Msg("Answer Updated")

	answers.UpdatedAnswers = append(answers.UpdatedAnswers, &testreporters.OCRAnswerUpdated{
		ContractAddress: answerAddress,
		ExpectedUpdate:  false,
		ExpectedRoundId: currentAnswer.OnChainRoundId + 1,
		ExpectedAnswer:  currentAnswer.ExpectedAnswer,
	})
	return currentAnswer.ExpectedUpdate
}

// triggers a new OCR round by setting a new mock adapter value
func (t *OCRSoakTest) triggerNewRound(currentAdapterValue int) {
	startingBlockNum, err := t.chainClient.LatestBlockNumber(context.Background())
	Expect(err).ShouldNot(HaveOccurred(), "Error retrieving latest block number")
	for addr, responses := range t.TestReporter.ContractReports {
		pendingResponse := responses.UpdatedAnswers[len(responses.UpdatedAnswers)-1]
		pendingResponse.ExpectedUpdate = true
		pendingResponse.StartingBlockNum = startingBlockNum
		pendingResponse.ExpectedAnswer = currentAdapterValue
		pendingResponse.StartingTime = time.Now()
		log.Debug().
			Str("Address", addr).
			Uint64("Expected Round ID", pendingResponse.ExpectedRoundId).
			Int("Expected Answer", pendingResponse.ExpectedAnswer).
			Msg("Setting Round Update to Expected")
	}
	actions.SetAllAdapterResponsesToTheSameValue(currentAdapterValue, t.ocrInstances, t.chainlinkNodes, t.mockServer)()
	log.Info().
		Int("Value", currentAdapterValue).
		Msg("Starting a New OCR Round")
}

func (t *OCRSoakTest) processNewEvent(
	eventSub geth.Subscription,
	answerUpdated chan *ethereum.OffchainAggregatorAnswerUpdated,
	vLog types.Log,
	ocrInstance contracts.OffChainAggregator,
	contractABI *abi.ABI,
) {
	defer GinkgoRecover()

	eventDetails, err := contractABI.EventByID(vLog.Topics[0])
	Expect(err).ShouldNot(HaveOccurred(), "Getting event details for OCR instances shouldn't fail")

	errorChan := make(chan error)
	eventConfirmed := make(chan bool)
	err = t.chainClient.ProcessEvent(eventDetails.Name, vLog, eventConfirmed, errorChan)
	Expect(err).ShouldNot(HaveOccurred(), "Error attempting to confirm Event")
	for {
		select {
		case err := <-errorChan:
			Expect(err).ShouldNot(HaveOccurred(), "Error while confirming Event")
			return
		case confirmed := <-eventConfirmed:
			if confirmed {
				if eventDetails.Name == "AnswerUpdated" { // Send AnswerUpdated events to answerUpdated channel to handle in main loop
					answer, err := ocrInstance.ParseAnswerUpdated(vLog)
					Expect(err).ShouldNot(HaveOccurred(), "Parsing AnswerUpdated event log in OCR instance shouldn't fail")
					answerUpdated <- answer
				}
				log.Info().
					Str("Contract", vLog.Address.Hex()).
					Str("Event Name", eventDetails.Name).
					Uint64("Block Number", vLog.BlockNumber).
					Msg("Contract event published")
			}
			return
		}
	}
}

// ensureValues ensures that all values needed to run the test are present
func (t *OCRSoakTest) ensureInputValues() {
	inputs := t.Inputs
	Expect(inputs.BlockchainClient).ShouldNot(BeNil(), "Need a valid blockchain client to use for the test")
	t.chainClient = inputs.BlockchainClient
	Expect(inputs.NumberOfContracts).Should(BeNumerically(">=", 1), "Expecting at least 1 OCR contract")
	Expect(inputs.ChainlinkNodeFunding.Float64()).Should(BeNumerically(">", 0), "Expecting non-zero chainlink node funding amount")
	Expect(inputs.TestDuration).Should(BeNumerically(">=", time.Minute*1), "Expected test duration to be more than a minute")
	Expect(inputs.ExpectedRoundTime).Should(BeNumerically(">=", time.Second*1), "Expected ExpectedRoundTime to be greater than 1 second")
	Expect(inputs.TimeBetweenRounds).ShouldNot(BeNil(), "You forgot to set TimeBetweenRounds")
	Expect(inputs.TimeBetweenRounds).Should(BeNumerically("<", time.Hour), "TimeBetweenRounds must be less than 1 hour")
}
