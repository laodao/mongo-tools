// Package bsondump converts BSON files into human-readable formats such as JSON.
package bsondump

import (
	"bytes"
	"fmt"
	"github.com/mongodb/mongo-tools/common/bsonutil"
	"github.com/mongodb/mongo-tools/common/db"
	"github.com/mongodb/mongo-tools/common/json"
	"github.com/mongodb/mongo-tools/common/log"
	"github.com/mongodb/mongo-tools/common/options"
	"gopkg.in/mgo.v2/bson"
	"io"
	"os"
	"strings"
)

// BSONDump is a container for the user-specified options and
// internal state used for running bsondump.
type BSONDump struct {
	// generic mongo tool options
	ToolOptions *options.ToolOptions

	// BSONDumpOptions defines options used to control how BSON data is displayed
	BSONDumpOptions *BSONDumpOptions

	// Path to the BSON file
	FileName string

	// Handle to where the BSON data should be displayed
	Out io.Writer

	bsonSource *db.BSONSource
}

// Open opens the relevant file for reading. It returns a
// non-nil error if it is unable to open the file.
func (bd *BSONDump) Open() error {
	file, err := os.Open(bd.FileName)
	if err != nil {
		return fmt.Errorf("couldn't open BSON file: %v", err)
	}
	bd.bsonSource = db.NewBSONSource(file)
	return nil
}

func printJSON(doc *bson.Raw, out io.Writer, pretty bool) error {
	decodedDoc := bson.M{}
	err := bson.Unmarshal(doc.Data, &decodedDoc)
	if err != nil {
		return err
	}

	extendedDoc, err := bsonutil.ConvertBSONValueToJSON(decodedDoc)
	if err != nil {
		return fmt.Errorf("error converting BSON to extended JSON: %v", err)
	}
	jsonBytes, err := json.Marshal(extendedDoc)
	if pretty {
		var jsonFormatted bytes.Buffer
		json.Indent(&jsonFormatted, jsonBytes, "", "\t")
		jsonBytes = jsonFormatted.Bytes()
	}
	if err != nil {
		return fmt.Errorf("error converting doc to JSON: %v", err)
	}
	_, err = out.Write(jsonBytes)
	return err
}

// JSON iterates through the BSON file and for each document it finds,
// recursively descends into objects and arrays and prints the human readable
// JSON representation.
// It returns the number of documents processed and a non-nil error if one is
// encountered before the end of the file is reached.
func (bd *BSONDump) JSON() (int, error) {
	numFound := 0

	if bd.bsonSource == nil {
		panic("Tried to call JSON() before opening file")
	}

	decodedStream := db.NewDecodedBSONSource(bd.bsonSource)
	defer decodedStream.Close()

	var result bson.Raw
	for decodedStream.Next(&result) {
		if err := printJSON(&result, bd.Out, bd.BSONDumpOptions.Pretty); err != nil {
			log.Logf(log.Always, "unable to dump document %v: %v", numFound+1, err)

			//if objcheck is turned on, stop now. otherwise keep on dumpin'
			if bd.BSONDumpOptions.ObjCheck {
				return numFound, err
			}
		} else {
			_, err := bd.Out.Write([]byte("\n"))
			if err != nil {
				return numFound, err
			}
		}
		numFound++
	}
	if err := decodedStream.Err(); err != nil {
		return numFound, err
	}
	return numFound, nil
}

// Debug iterates through the BSON file and for each document it finds,
// recursively descends into objects and arrays and prints a human readable
// BSON representation containing the type and size of each field.
// It returns the number of documents processed and a non-nil error if one is
// encountered before the end of the file is reached.
func (bd *BSONDump) Debug() (int, error) {
	numFound := 0

	if bd.bsonSource == nil {
		panic("Tried to call Debug() before opening file")
	}

	defer bd.bsonSource.Close()

	reusableBuf := make([]byte, db.MaxBSONSize)
	var result bson.Raw
	for {
		hasDoc, docSize := bd.bsonSource.LoadNextInto(reusableBuf)
		if !hasDoc {
			break
		}
		result.Data = reusableBuf[0:docSize]

		if bd.BSONDumpOptions.ObjCheck {
			validated := bson.M{}
			err := bson.Unmarshal(result.Data, &validated)
			if err != nil {
				// ObjCheck is turned on and we hit an error, so short-circuit now.
				return numFound, fmt.Errorf("failed to validate bson during objcheck: %v", err)
			}
		}
		err := printBSON(result, 0, bd.Out)
		if err != nil {
			log.Logf(log.Always, "encountered error debugging BSON data: %v", err)
		}
		numFound++
	}

	if err := bd.bsonSource.Err(); err != nil {
		// This error indicates the BSON document header is corrupted;
		// either the 4-byte header couldn't be read in full, or
		// the size in the header would require reading more bytes
		// than the file has left
		return numFound, err
	}
	return numFound, nil
}

func printBSON(raw bson.Raw, indentLevel int, out io.Writer) error {
	indent := strings.Repeat("\t", indentLevel)
	fmt.Fprintf(out, "%v--- new object ---\n", indent)
	fmt.Fprintf(out, "%v\tsize : %v\n", indent, len(raw.Data))

	//Convert raw into an array of RawD we can iterate over.
	var rawD bson.RawD
	err := bson.Unmarshal(raw.Data, &rawD)
	if err != nil {
		return err
	}
	for _, rawElem := range rawD {
		fmt.Fprintf(out, "%v\t\t%v\n", indent, rawElem.Name)

		// the size of an element is the combined size of the following:
		// 1. 1 byte for the BSON type
		// 2. 'e_name' : the BSON key, which is a null-terminated cstring
		// 3. The BSON value
		// So size == 1 [size of type byte] +  1 [null byte for cstring key] + len(bson key) + len(bson value)
		// see http://bsonspec.org/spec.html for more details
		fmt.Fprintf(out, "%v\t\t\ttype: %4v size: %v\n", indent, int8(rawElem.Value.Kind),
			2+len(rawElem.Name)+len(rawElem.Value.Data))

		//For nested objects or arrays, recurse.
		if rawElem.Value.Kind == 0x03 || rawElem.Value.Kind == 0x04 {
			err = printBSON(rawElem.Value, indentLevel+3, out)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
