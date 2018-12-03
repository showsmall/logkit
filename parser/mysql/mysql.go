package mysql

import (
	"sort"
	"strings"
	"sync"

	"github.com/Preetam/mysqllog"

	"github.com/qiniu/logkit/conf"
	"github.com/qiniu/logkit/parser"
	. "github.com/qiniu/logkit/parser/config"
	. "github.com/qiniu/logkit/utils/models"
)

func init() {
	parser.RegisterConstructor(TypeMySQL, NewParser)
}

type Parser struct {
	name                 string
	ps                   *mysqllog.Parser
	labels               []GrokLabel
	disableRecordErrData bool
	keepRawData          bool
	rawDatas             []string
	numRoutine           int
}

func NewParser(c conf.MapConf) (parser.Parser, error) {
	name, _ := c.GetStringOr(KeyParserName, "")
	labelList, _ := c.GetStringListOr(KeyLabels, []string{})

	nameMap := make(map[string]struct{})
	labels := GetGrokLabels(labelList, nameMap)

	disableRecordErrData, _ := c.GetBoolOr(KeyDisableRecordErrData, false)
	keepRawData, _ := c.GetBoolOr(KeyKeepRawData, false)
	numRoutine := MaxProcs
	if numRoutine == 0 {
		numRoutine = 1
	}

	return &Parser{
		name:                 name,
		labels:               labels,
		disableRecordErrData: disableRecordErrData,
		ps:                   &mysqllog.Parser{},
		keepRawData:          keepRawData,
		numRoutine:           numRoutine,
	}, nil
}

func (p *Parser) Name() string {
	return p.name
}

func (p *Parser) Type() string {
	return TypeMySQL
}

func (p *Parser) parse(line string) (d Data, err error) {
	if line == PandoraParseFlushSignal {
		return p.Flush()
	}
	if p.keepRawData {
		p.rawDatas = append(p.rawDatas, line)
	}
	event := p.ps.ConsumeLine(line)
	if event == nil {
		return
	}
	d = make(Data, len(event)+len(p.labels)+1)
	for k, v := range event {
		d[k] = v
	}
	for _, l := range p.labels {
		d[l.Name] = l.Value
	}
	if p.keepRawData {
		d[KeyRawData] = strings.Join(p.rawDatas, "\n")
		p.rawDatas = p.rawDatas[:0:0]
	}
	return d, nil
}
func (p *Parser) Parse(lines []string) ([]Data, error) {
	var (
		datas = make([]Data, 0, len(lines))
		se    = &StatsError{}
	)
	numRoutine := p.numRoutine
	if len(lines) < numRoutine {
		numRoutine = len(lines)
	}
	sendChan := make(chan parser.ParseInfo)
	resultChan := make(chan parser.ParseResult)

	wg := new(sync.WaitGroup)
	for i := 0; i < numRoutine; i++ {
		wg.Add(1)
		go parser.ParseLine(sendChan, resultChan, wg, true, p.parse)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	go func() {
		for idx, line := range lines {
			sendChan <- parser.ParseInfo{
				Line:  line,
				Index: idx,
			}
		}
		close(sendChan)
	}()

	var parseResultSlice = make(parser.ParseResultSlice, 0, len(lines))
	for resultInfo := range resultChan {
		parseResultSlice = append(parseResultSlice, resultInfo)
	}
	if numRoutine > 1 {
		sort.Stable(parseResultSlice)
	}

	for _, parseResult := range parseResultSlice {
		if len(parseResult.Line) == 0 {
			se.DatasourceSkipIndex = append(se.DatasourceSkipIndex, parseResult.Index)
			continue
		}

		if parseResult.Err != nil {
			se.AddErrors()
			se.LastError = parseResult.Err.Error()
			errData := make(Data)
			if !p.disableRecordErrData {
				errData[KeyPandoraStash] = parseResult.Line
			} else if !p.keepRawData {
				se.DatasourceSkipIndex = append(se.DatasourceSkipIndex, parseResult.Index)
			}
			if p.keepRawData {
				errData[KeyRawData] = parseResult.Line
			}
			if !p.disableRecordErrData || p.keepRawData {
				datas = append(datas, errData)
			}
			continue
		}
		if len(parseResult.Data) < 1 { //数据为空时不发送
			continue
		}
		se.AddSuccess()
		datas = append(datas, parseResult.Data)
	}

	if se.Errors == 0 {
		return datas, nil
	}
	return datas, se
}

func (p *Parser) Flush() (data Data, err error) {
	data = Data(p.ps.Flush())
	return
}
