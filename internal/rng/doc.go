// Package rng validates XML against RELAX NG schemas written in the compact
// syntax.
//
// It implements the derivative-based algorithm of James Clark's "An algorithm
// for RELAX NG validation", the approach of Jing, which EPUBCheck uses.
// Patterns are hash-consed and choices kept as sorted sets, so equal patterns
// are one value and derivatives can be memoised by identity.
//
// The datatypes supported are RELAX NG's built-in string and token and the
// XML Schema types that EPUB's schemas use. Anything else is accepted, so an
// unknown datatype never produces a false error.
package rng
