package xlsx

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pbnjay/grate"
	"github.com/pbnjay/grate/commonxl"
)

type Sheet struct {
	err     error
	d       *Document
	relID   string
	name    string
	docname string
	rows    []*row
	minRow  int
	maxRow  int
	minCol  int
	maxCol  int
	iterRow int
	empty   bool
}

var errNotLoaded = errors.New("xlsx: sheet not loaded")

type row struct {
	// each value must be one of: int, float64, string, or time.Time
	cols []commonxl.Value
}

func (s *Sheet) parseSheet() error {
	linkmap := make(map[string]string)
	base := filepath.Base(s.docname)
	sub := strings.TrimSuffix(s.docname, base)
	relsname := filepath.Join(sub, "_rels", base+".rels")
	dec, clo, err := s.d.openXML(relsname)
	if err == nil {
		// rels might not exist for every sheet
		tok, err := dec.RawToken()
		for ; err == nil; tok, err = dec.RawToken() {
			if v, ok := tok.(xml.StartElement); ok && v.Name.Local == "Relationship" {
				ax := getAttrs(v.Attr, "Id", "Type", "Target", "TargetMode")
				if ax[3] == "External" && ax[1] == "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" {
					linkmap[ax[0]] = ax[2]
				}
			}
		}
		clo.Close()
	}

	dec, clo, err = s.d.openXML(s.docname)
	if err != nil {
		return err
	}
	defer clo.Close()

	currentCellType := BlankCellType
	currentCell := ""
	var numFormat commonxl.FmtFunc
	tok, err := dec.RawToken()
	for ; err == nil; tok, err = dec.RawToken() {
		switch v := tok.(type) {
		case xml.CharData:
			if currentCell == "" {
				continue
			}
			c, r := refToIndexes(currentCell)
			if c >= 0 && r >= 0 {
				str := string(v)
				var val interface{} = str
				switch currentCellType {
				case BooleanCellType:
					if v[0] == '1' {
						val = true
					} else {
						val = false
					}
				case DateCellType:
					log.Println("CELL DATE", val, numFormat)
				case NumberCellType:
					if fval, err := strconv.ParseFloat(str, 64); err == nil {
						str, val = numFormat(&s.d.fmt, fval)
					}
					//log.Println("CELL NUMBER", val, numFormat)
				case SharedStringCellType:
					//log.Println("CELL SHSTR", val, currentCellType, numFormat)
					si, _ := strconv.ParseInt(string(v), 10, 64)
					str = s.d.strings[si]
					val = str
				case BlankCellType:
					//log.Println("CELL BLANK")
					// don't place any values
					continue
				case ErrorCellType, FormulaStringCellType, InlineStringCellType:
					//log.Println("CELL ERR/FORM/INLINE", val, currentCellType)
				default:
					log.Println("CELL UNKNOWN", val, currentCellType, numFormat)
				}
				//log.Println(r, c, val, str)
				s.placeValue(r, c, val, str)
			} else {
				//log.Println("FAIL row/col: ", currentCell)
			}
		case xml.StartElement:
			switch v.Name.Local {
			case "dimension":
				ax := getAttrs(v.Attr, "ref")
				if ax[0] == "A1" {
					// short-circuit empty sheet
					s.minCol, s.minRow = 0, 0
					s.maxCol, s.maxRow = 1, 1
					s.empty = true
					continue
				}
				dims := strings.Split(ax[0], ":")
				if len(dims) == 1 {
					s.minCol, s.minRow = 0, 0
					s.maxCol, s.maxRow = refToIndexes(dims[0])
				} else {
					s.minCol, s.minRow = refToIndexes(dims[0])
					s.maxCol, s.maxRow = refToIndexes(dims[1])
				}
				//log.Println("DIMENSION:", s.minRow, s.minCol, ">", s.maxRow, s.maxCol)
			case "row":
				//currentRow = ax["r"] // unsigned int row index
				//log.Println("ROW", currentRow)
			case "c":
				ax := getAttrs(v.Attr, "t", "r", "s")
				currentCellType = CellType(ax[0])
				if currentCellType == BlankCellType {
					currentCellType = NumberCellType
				}
				currentCell = ax[1] // always an A1 style reference
				style := ax[2]
				sid, _ := strconv.ParseInt(style, 10, 64)
				//log.Println(currentCellType, style, sid)
				if len(s.d.xfs) > int(sid) {
					numFormat = s.d.xfs[sid] // unsigned integer lookup
				} else {
					numFormat = s.d.xfs[0]
				}
				//log.Println("CELL", currentCell, sid, numFormat, currentCellType)
			case "v":
				//log.Println("CELL VALUE", ax)

			case "mergeCell":
				ax := getAttrs(v.Attr, "ref")
				dims := strings.Split(ax[0], ":")
				startCol, startRow := refToIndexes(dims[0])
				endCol, endRow := startCol, startRow
				if len(dims) > 1 {
					endCol, endRow = refToIndexes(dims[1])
				}
				for r := startRow; r <= endRow; r++ {
					for c := startCol; c <= endCol; c++ {
						if r == startRow && c == startCol {
							// has data already!
						} else if c == startCol {
							// first and last column MAY be the same
							if r == endRow {
								s.placeValue(r, c, endRowMerged, "")
							} else {
								s.placeValue(r, c, continueRowMerged, "")
							}
						} else if c == endCol {
							// first and last column are NOT the same
							s.placeValue(r, c, endColumnMerged, "")
						} else {
							s.placeValue(r, c, continueColumnMerged, "")
						}
					}
				}

			case "hyperlink":
				ax := getAttrs(v.Attr, "ref", "id")
				col, row := refToIndexes(ax[0])
				link := linkmap[ax[1]]
				if len(s.rows) > row && len(s.rows[row].cols) > col {
					if sstr, ok := s.rows[row].cols[col].Raw().(string); ok {
						link = sstr + " <" + link + ">"
					}
				}
				s.placeValue(row, col, link, link)

			case "worksheet", "mergeCells", "hyperlinks":
				// containers
			case "f":
				//log.Println("start: ", v.Name.Local, v.Attr)
			default:
				if grate.Debug {
					log.Println("      Unhandled sheet xml tag", v.Name.Local, v.Attr)
				}
			}
		case xml.EndElement:

			switch v.Name.Local {
			case "c":
				currentCell = ""
			case "row":
				//currentRow = ""
			}
		default:
			if grate.Debug {
				log.Printf("      Unhandled sheet xml tokens %T %+v", tok, tok)
			}
		}
	}
	if err == io.EOF {
		err = nil
	}
	return err
}

func (s *Sheet) placeValue(rowIndex, colIndex int, val interface{}, str string) {
	if colIndex > s.maxCol || rowIndex > s.maxRow {
		// invalid
		return
	}

	// ensure we always have a complete matrix
	for len(s.rows) <= rowIndex {
		emptyRow := make([]commonxl.Value, s.maxCol+1)
		s.rows = append(s.rows, &row{cols: emptyRow})
	}
	s.empty = false
	s.rows[rowIndex].cols[colIndex] = commonxl.NewValue(val, str)
}

// Next advances to the next row of content.
// It MUST be called prior to any Scan().
func (s *Sheet) Next() bool {
	s.iterRow++
	return s.iterRow < len(s.rows)
}

func (s *Sheet) Strings() []string {
	currow := s.rows[s.iterRow]
	res := make([]string, len(currow.cols))
	for i, col := range currow.cols {
		if col.IsEmpty() {
			continue
		}
		res[i] = fmt.Sprint(col)
	}
	return res
}

// Scan extracts values from the row into the provided arguments
// Arguments must be pointers to one of 5 supported types:
//     bool, int, float64, string, time.Time or interface{}
func (s *Sheet) Scan(args ...interface{}) error {
	currow := s.rows[s.iterRow]

	for i, a := range args {
		raw := currow.cols[i].Raw()
		switch v := a.(type) {
		case *bool:
			*v = raw.(bool)
		case *int:
			*v = raw.(int)
		case *float64:
			*v = raw.(float64)
		case *string:
			*v = raw.(string)
		case *time.Time:
			*v = raw.(time.Time)
		case *interface{}:
			*v = raw
		default:
			return grate.ErrInvalidScanType
		}
	}
	return nil
}

func (s *Sheet) IsEmpty() bool {
	return s.empty
}

// Err returns the last error that occured.
func (s *Sheet) Err() error {
	return s.err
}
