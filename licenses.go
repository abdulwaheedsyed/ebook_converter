package main

import _ "embed"

// The license and third-party notices travel inside the binary, so a copy
// distributed on its own still carries them. Print them with --licenses.

//go:embed LICENSE
var licenseText string

//go:embed THIRD_PARTY_NOTICES.md
var noticesText string
