package app

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tsiemens/acb/fx"
	"github.com/tsiemens/acb/log"
	ptf "github.com/tsiemens/acb/portfolio"
)

var AcbVersion = "0.5.0"

/* Takes a list of security status strings, each formatted as:
 * SYM:nShares:totalAcb. Eg. GOOG:20:1000.00
 */
func ParseInitialStatus(
	initialSecurityStates []string) (map[string]*ptf.PortfolioSecurityStatus, error) {
	stati := make(map[string]*ptf.PortfolioSecurityStatus)
	for _, opt := range initialSecurityStates {
		parts := strings.Split(opt, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("Invalid ACB format '%s'", opt)
		}
		symbol := parts[0]
		shares, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("Invalid shares format '%s'. %v", opt, err)
		}
		acb, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return nil, fmt.Errorf("Invalid ACB format '%s'. %v", opt, err)
		}

		if _, ok := stati[symbol]; ok {
			return nil, fmt.Errorf("Symbol %s specified multiple times", symbol)
		}
		stati[symbol] = &ptf.PortfolioSecurityStatus{
			Security: symbol, ShareBalance: uint32(shares), TotalAcb: acb}
	}
	return stati, nil
}

type DescribedReader struct {
	Desc   string
	Reader io.Reader
}

type LegacyOptions struct {
	NoSuperficialLosses        bool
	NoPartialSuperficialLosses bool
	SortBuysBeforeSells        bool
}

func NewLegacyOptions() LegacyOptions {
	return LegacyOptions{
		NoSuperficialLosses:        false,
		NoPartialSuperficialLosses: false,
		SortBuysBeforeSells:        false,
	}
}

type SecurityDeltas struct {
	Deltas []*ptf.TxDelta
	Errors []error
}

func RunAcbAppToDeltaModels(
	csvFileReaders []DescribedReader,
	allInitStatus map[string]*ptf.PortfolioSecurityStatus,
	forceDownload bool,
	legacyOptions LegacyOptions,
	ratesCache fx.RatesCache,
	errPrinter log.ErrorPrinter) (map[string]*SecurityDeltas, error) {

	rateLoader := fx.NewRateLoader(forceDownload, ratesCache, errPrinter)

	allTxs := make([]*ptf.Tx, 0, 20)
	var globalReadIndex uint32 = 0
	for _, csvReader := range csvFileReaders {
		txs, err := ptf.ParseTxCsv(csvReader.Reader, globalReadIndex, csvReader.Desc, rateLoader)
		if err != nil {
			return nil, err
		}
		globalReadIndex += uint32(len(txs))
		for _, tx := range txs {
			allTxs = append(allTxs, tx)
		}
	}

	allTxs = ptf.SortTxs(allTxs, legacyOptions.SortBuysBeforeSells)
	txsBySec := ptf.SplitTxsBySecurity(allTxs)

	portfolioLegacyOptions := ptf.LegacyOptions{
		NoSuperficialLosses:        legacyOptions.NoSuperficialLosses,
		NoPartialSuperficialLosses: legacyOptions.NoPartialSuperficialLosses,
	}
	models := make(map[string]*SecurityDeltas)

	for sec, secTxs := range txsBySec {
		secInitStatus, ok := allInitStatus[sec]
		if !ok {
			secInitStatus = nil
		}
		deltas, err := ptf.TxsToDeltaList(secTxs, secInitStatus, portfolioLegacyOptions)
		deltasModel := &SecurityDeltas{deltas, []error{}}
		if err != nil {
			deltasModel.Errors = append(deltasModel.Errors, err)
		}
		models[sec] = deltasModel
	}
	return models, nil
}

func RunAcbAppToRenderModel(
	csvFileReaders []DescribedReader,
	allInitStatus map[string]*ptf.PortfolioSecurityStatus,
	forceDownload bool,
	renderFullDollarValues bool,
	legacyOptions LegacyOptions,
	ratesCache fx.RatesCache,
	errPrinter log.ErrorPrinter) (map[string]*ptf.RenderTable, error) {

	deltasBySec, err := RunAcbAppToDeltaModels(
		csvFileReaders, allInitStatus, forceDownload, legacyOptions, ratesCache,
		errPrinter)
	if err != nil {
		return nil, err
	}

	models := make(map[string]*ptf.RenderTable)
	for sec, deltas := range deltasBySec {
		tableModel := ptf.RenderTxTableModel(deltas.Deltas, renderFullDollarValues)
		tableModel.Errors = deltas.Errors
		models[sec] = tableModel
	}
	return models, nil
}

func WriteRenderTables(
	renderTables map[string]*ptf.RenderTable,
	writer io.Writer) {

	nSecs := len(renderTables)

	secs := make([]string, 0, len(renderTables))
	for k := range renderTables {
		secs = append(secs, k)
	}
	sort.Strings(secs)

	i := 0
	for _, sec := range secs {
		renderTable := renderTables[sec]
		for _, err := range renderTable.Errors {
			fmt.Fprintf(writer, "[!] %v. Printing parsed information state:\n", err)
		}
		fmt.Fprintf(writer, "Transactions for %s\n", sec)
		ptf.PrintRenderTable(renderTable, writer)
		if i < (nSecs - 1) {
			fmt.Fprintln(writer, "")
		}
		i++
	}
}

// Returns an OK flag. Used to signal what exit code to use.
// All errors get printed to the errPrinter or to the writer (as appropriate).
func RunAcbAppToWriter(
	writer io.Writer,
	csvFileReaders []DescribedReader,
	allInitStatus map[string]*ptf.PortfolioSecurityStatus,
	forceDownload bool,
	renderFullDollarValues bool,
	legacyOptions LegacyOptions,
	ratesCache fx.RatesCache,
	errPrinter log.ErrorPrinter) (bool, map[string]*ptf.RenderTable) {

	renderTables, err := RunAcbAppToRenderModel(
		csvFileReaders, allInitStatus, forceDownload, renderFullDollarValues,
		legacyOptions, ratesCache, errPrinter,
	)

	if err != nil {
		errPrinter.Ln("Error:", err)
		return false, nil
	}

	WriteRenderTables(renderTables, writer)
	return true, renderTables
}

// Returns an OK flag. Used to signal what exit code to use.
func RunAcbAppToConsole(
	csvFileReaders []DescribedReader,
	allInitStatus map[string]*ptf.PortfolioSecurityStatus,
	forceDownload bool,
	renderFullDollarValues bool,
	legacyOptions LegacyOptions,
	ratesCache fx.RatesCache,
	errPrinter log.ErrorPrinter) bool {

	ok, _ := RunAcbAppToWriter(
		os.Stdout,
		csvFileReaders, allInitStatus, forceDownload, renderFullDollarValues,
		legacyOptions, ratesCache, errPrinter,
	)
	return ok
}
