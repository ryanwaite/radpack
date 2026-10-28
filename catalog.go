package radpack

import _ "embed"

// Catalog is bundled into the executable so discovery never depends on the
// working directory or a mutable remote catalog.
//
//go:embed catalog.json
var Catalog []byte
