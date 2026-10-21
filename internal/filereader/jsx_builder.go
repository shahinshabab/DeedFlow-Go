package filereader

import _ "embed"

// jsxBuilder is the script body that builds an InDesign document from DOCS
// (copied verbatim from the Django project's jsx_writer.py).
//
//go:embed jsx_builder.js
var jsxBuilder string
