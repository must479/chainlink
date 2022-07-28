package testreporters

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/rs/zerolog/log"
	"github.com/slack-go/slack"

	"github.com/smartcontractkit/chainlink-testing-framework/testreporters"
)

// OCRSoakTestReporter collates all OCRAnswerUpdated events into a single report
type OCRSoakTestReporter struct {
	ContractReports       map[string]*OCRReport // contractAddress: Answers
	ExpectedRoundDuration time.Duration
	UnexpectedShutdown    bool
	AnomaliesDetected     bool

	namespace   string
	csvLocation string
}

// OCRReport holds all answered rounds and summary data for an OCR contract
type OCRReport struct {
	ContractAddress        string
	UpdatedAnswers         []*OCRAnswerUpdated
	AnomalousAnswerIndexes []int
	ExpectedRoundDuration  time.Duration

	totalRounds         uint64
	longestRoundTime    time.Duration
	shortestRoundTime   time.Duration
	averageRoundTime    time.Duration
	longestRoundBlocks  uint64
	shortestRoundBlocks uint64
	averageRoundBlocks  uint64
}

// ProcessOCRReport summarizes all data collected from OCR rounds
func (o *OCRReport) ProcessOCRReport() bool {
	log.Debug().Str("OCR Address", o.ContractAddress).Msg("Processing OCR Soak Report")
	o.AnomalousAnswerIndexes = make([]int, 0)

	var (
		totalRoundBlocks uint64
		totalRoundTime   time.Duration
	)

	firstAnswer := o.UpdatedAnswers[0]
	o.longestRoundTime = firstAnswer.RoundDuration
	o.shortestRoundTime = firstAnswer.RoundDuration
	o.longestRoundBlocks = firstAnswer.BlockDuration
	o.shortestRoundBlocks = firstAnswer.BlockDuration
	for index, updatedAnswer := range o.UpdatedAnswers {
		if updatedAnswer.ProcessAnomalies(o.ExpectedRoundDuration) {
			o.AnomalousAnswerIndexes = append(o.AnomalousAnswerIndexes, index)
		}
		o.totalRounds++
		totalRoundTime += updatedAnswer.RoundDuration
		totalRoundBlocks += updatedAnswer.BlockDuration
		if o.longestRoundTime < updatedAnswer.RoundDuration {
			o.longestRoundTime = updatedAnswer.RoundDuration
		}
		if o.shortestRoundTime > updatedAnswer.RoundDuration {
			o.shortestRoundTime = updatedAnswer.RoundDuration
		}
		if o.longestRoundBlocks < updatedAnswer.BlockDuration {
			o.longestRoundBlocks = updatedAnswer.BlockDuration
		}
		if o.shortestRoundBlocks > updatedAnswer.BlockDuration {
			o.shortestRoundBlocks = updatedAnswer.BlockDuration
		}
	}
	o.averageRoundBlocks = totalRoundBlocks / o.totalRounds
	o.averageRoundTime = totalRoundTime / time.Duration(o.totalRounds)
	return len(o.AnomalousAnswerIndexes) > 0
}

// OCRAnswerUpdated records details of an OCRAnswerUpdated event and compares them against expectations
type OCRAnswerUpdated struct {
	// metadata
	ContractAddress  string
	ExpectedUpdate   bool
	StartingBlockNum uint64
	BlockDuration    uint64
	StartingTime     time.Time
	RoundDuration    time.Duration

	// round data
	ExpectedRoundId uint64
	ExpectedAnswer  int

	UpdatedRoundId  uint64
	UpdatedBlockNum uint64
	UpdatedTime     time.Time
	UpdatedAnswer   int

	OnChainRoundId uint64
	OnChainAnswer  int

	Anomalous bool
	Anomalies []string
}

// ProcessAnomalies checks received data against expected data of the updated answer, returning if anything mismatches
func (o *OCRAnswerUpdated) ProcessAnomalies(expectedRoundDuration time.Duration) bool {
	var isAnomaly bool
	anomalies := []string{}

	if !o.ExpectedUpdate {
		isAnomaly = true
		anomalies = append(anomalies, "Unexpected new round, possible double transmission")
	}
	if o.ExpectedRoundId != o.UpdatedRoundId || o.ExpectedRoundId != o.OnChainRoundId {
		isAnomaly = true
		anomalies = append(anomalies, "RoundID mismatch, possible double transmission")
	}
	if o.ExpectedAnswer != o.UpdatedAnswer || o.ExpectedAnswer != o.OnChainAnswer {
		isAnomaly = true
		anomalies = append(anomalies, "! ANSWER MISMATCH !")
	}
	if o.RoundDuration > expectedRoundDuration {
		isAnomaly = true
		anomalies = append(anomalies, fmt.Sprintf("Round took %s to complete, longer than expected", expectedRoundDuration.String()))
	}
	o.Anomalous, o.Anomalies = isAnomaly, anomalies
	return isAnomaly
}

func (o *OCRAnswerUpdated) toCSV() []string {
	return []string{
		o.ContractAddress, fmt.Sprint(o.ExpectedUpdate), o.StartingTime.Truncate(time.Second).String(),
		fmt.Sprint(o.ExpectedRoundId), fmt.Sprint(o.UpdatedRoundId), fmt.Sprint(o.OnChainRoundId),
		o.UpdatedTime.Truncate(time.Second).String(), o.RoundDuration.Truncate(time.Second).String(),
		fmt.Sprint(o.StartingBlockNum), fmt.Sprint(o.UpdatedBlockNum), fmt.Sprint(o.BlockDuration),
		fmt.Sprint(o.ExpectedAnswer), fmt.Sprint(o.UpdatedAnswer), fmt.Sprint(o.OnChainAnswer),
		fmt.Sprint(o.Anomalous), strings.Join(o.Anomalies, " | "),
	}
}

// SetNamespace sets the namespace of the report for clean reports
func (o *OCRSoakTestReporter) SetNamespace(namespace string) {
	o.namespace = namespace
}

// WriteReport writes OCR Soak test report to logs
func (o *OCRSoakTestReporter) WriteReport(folderLocation string) error {
	log.Debug().Msg("Writing OCR Soak Test Report")
	var reportGroup sync.WaitGroup
	for _, report := range o.ContractReports {
		reportGroup.Add(1)
		go func(report *OCRReport) {
			defer reportGroup.Done()
			if report.ProcessOCRReport() {
				o.AnomaliesDetected = true
			}
		}(report)
	}
	reportGroup.Wait()
	log.Debug().Int("Count", len(o.ContractReports)).Msg("Processed OCR Soak Test Reports")
	return o.writeCSV(folderLocation)
}

// SendNotification sends a slack message to a slack webhook and uploads test artifacts
func (o *OCRSoakTestReporter) SendSlackNotification(slackClient *slack.Client) error {
	if slackClient == nil {
		slackClient = slack.New(testreporters.SlackAPIKey)
	}

	testFailed := ginkgo.CurrentSpecReport().Failed()
	headerText := ":white_check_mark: OCR Soak Test PASSED :white_check_mark:"
	if testFailed {
		headerText = ":x: OCR Soak Test FAILED :x:"
	} else if o.AnomaliesDetected {
		headerText = ":x: OCR Soak Test Anomalies Detected :x:"
	}
	messageBlocks := testreporters.CommonSlackNotificationBlocks(slackClient, headerText, o.namespace, o.csvLocation, testreporters.SlackUserID, testFailed)
	ts, err := testreporters.SendSlackMessage(slackClient, slack.MsgOptionBlocks(messageBlocks...))
	if err != nil {
		return err
	}

	return testreporters.UploadSlackFile(slackClient, slack.FileUploadParameters{
		Title:           fmt.Sprintf("OCR Soak Test Report %s", o.namespace),
		Filetype:        "csv",
		Filename:        fmt.Sprintf("ocr_soak_%s.csv", o.namespace),
		File:            o.csvLocation,
		InitialComment:  fmt.Sprintf("OCR Soak Test Report %s.", o.namespace),
		Channels:        []string{testreporters.SlackChannel},
		ThreadTimestamp: ts,
	})
}

// writes a CSV report on the test runner
func (o *OCRSoakTestReporter) writeCSV(folderLocation string) error {
	reportLocation := filepath.Join(folderLocation, "./ocr_soak_report.csv")
	log.Debug().Str("Location", reportLocation).Msg("Writing OCR report")
	o.csvLocation = reportLocation
	ocrReportFile, err := os.Create(reportLocation)
	if err != nil {
		return err
	}
	defer ocrReportFile.Close()

	ocrReportWriter := csv.NewWriter(ocrReportFile)

	err = ocrReportWriter.Write([]string{
		"Contract Address",
		"Total Rounds Processed",
		"Average Round Time",
		"Longest Round Time",
		"Shortest Round Time",
		"Average Round Blocks",
		"Longest Round Blocks",
		"Shortest Round Blocks",
	})
	if err != nil {
		return err
	}
	for contractAddress, report := range o.ContractReports {
		err = ocrReportWriter.Write([]string{
			contractAddress,
			fmt.Sprint(report.totalRounds),
			report.averageRoundTime.Truncate(time.Second).String(),
			report.longestRoundTime.Truncate(time.Second).String(),
			report.shortestRoundTime.Truncate(time.Second).String(),
			fmt.Sprint(report.averageRoundBlocks),
			fmt.Sprint(report.longestRoundBlocks),
			fmt.Sprint(report.shortestRoundBlocks),
		})
		if err != nil {
			return err
		}
	}
	roundsTitle := []string{
		"Contract Address",
		"Update Expected",
		"Expected Round ID",
		"Event Round ID",
		"On-Chain Round ID",
		"Round Start Time",
		"Round End Time",
		"Round Duration",
		"Round Starting Block Number",
		"Round Ending Block Number",
		"Round Block Duration",
		"Expected Answer",
		"Event Answer",
		"On-Chain Answer",
		"Anomalous?",
		"Anomalies",
	}

	err = ocrReportWriter.Write([]string{})
	if err != nil {
		return err
	}

	err = ocrReportWriter.Write([]string{"Updates With Anomalies"})
	if err != nil {
		return err
	}

	err = ocrReportWriter.Write(roundsTitle)
	if err != nil {
		return err
	}

	for addr, report := range o.ContractReports {
		log.Debug().Str("OCR Address", addr).Msg("Checking for Anomalies")
		for _, anomalyIndex := range report.AnomalousAnswerIndexes {
			err = ocrReportWriter.Write(report.UpdatedAnswers[anomalyIndex].toCSV())
			if err != nil {
				return err
			}
		}
	}

	err = ocrReportWriter.Write([]string{})
	if err != nil {
		return err
	}

	err = ocrReportWriter.Write([]string{"All Updated Answers"})
	if err != nil {
		return err
	}
	err = ocrReportWriter.Write(roundsTitle)
	if err != nil {
		return err
	}

	for _, report := range o.ContractReports {
		for _, updatedAnswer := range report.UpdatedAnswers {
			err = ocrReportWriter.Write(updatedAnswer.toCSV())
			if err != nil {
				return err
			}
		}
	}

	ocrReportWriter.Flush()

	log.Info().Str("Location", reportLocation).Msg("Wrote CSV file")
	return nil
}
