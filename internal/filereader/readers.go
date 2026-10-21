package filereader

import (
	"bytes"
	"strings"
)

// PageMakerExtensions are the file extensions of PageMaker documents.
var PageMakerExtensions = []string{".pmd", ".p65", ".pm6", ".pm65", ".t65", ".pt6"}

// Accepted are the extensions the upload forms offer.
var Accepted = append(append([]string(nil), PageMakerExtensions...), ".idml", ".pdf")

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// IsPageMakerName says whether a file name has a PageMaker extension.
func IsPageMakerName(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range PageMakerExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// ReadFile picks the reader for an uploaded file and reads it.
func ReadFile(data []byte, filename string) (*Doc, error) {
	name := strings.ToLower(filename)
	head := data
	if len(head) > 8 {
		head = head[:8]
	}
	if strings.HasSuffix(name, ".indd") || strings.HasSuffix(name, ".indt") {
		return nil, readerErr("InDesign's own .indd format can't be read directly. In InDesign, use " +
			"File > Export and choose \"InDesign Markup (IDML)\", then upload the .idml file.")
	}
	var doc *Doc
	var err error
	switch {
	case bytes.HasPrefix(head, []byte("%PDF")):
		doc, err = ReadPDF(data)
	case bytes.HasPrefix(head, []byte("PK")):
		doc, err = ReadIDML(data)
	case bytes.HasPrefix(head, oleMagic) || IsPageMakerName(name):
		doc, err = ReadPageMaker(data, true)
	default:
		return nil, readerErr("This file type isn't supported. Upload a PageMaker file (.pmd, .p65), " +
			"an InDesign IDML export (.idml) or a PDF.")
	}
	if err != nil {
		return nil, err
	}
	doc.Name = filename
	doc.Rounded()
	return doc, nil
}
