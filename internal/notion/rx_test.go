package notion

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	const file = "src/atrium/notion_markdown.py:"
	rxtest.Run(t, map[string]*rx.Regexp{
		file + "CODE": codeRe, file + "ATTR": attrRe, file + "REF": refRe, file + "MEDIA": mediaRe,
		file + "DATE": dateRe, file + "TABLE": tableRe, file + "ROW": rowRe, file + "CELL": cellRe,
		file + "EMPTY": emptyRe, file + "BREAK": breakRe, file + "SUMMARY": summary, file + "WRAPPER": wrapper,
		file + "BLOCK_ATTRS": blockAtt, file + "IMAGE": imageRe, file + "UNESCAPED_PIPE": pipeRe,
		file + "LINK": linkRe, file + "HEADING": headingRe, file + "LIST_MARK": listMark, file + "QUOTE": quoteRe,
		file + "RULE": ruleRe, file + "EMPHASIS": emphasisRe, file + "TAG": tagRe, file + "CUT_TAG": cutTagRe,
		file + "ESCAPE": escapeRe,
	})
}
