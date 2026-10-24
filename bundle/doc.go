// Package bundle defines the import bundle: the format that a converter writes
// to move a board into gobb.
//
// A bundle is a directory, or a gzip-compressed tar archive of one. It holds
// "manifest.json", "settings.json", 23 JSON Lines data files and a "files"
// directory. This package holds a Go type for every file and every row, and the
// checks that need only one row. The checks are the string rules, the enums, the
// ranges, the exactly-one rules, the scope fit of an option grant and the shape
// of a file path.
//
// docs/bundle.md in the gobb repository is the format reference. It lists every
// file and every field, and it gives one example line for each data file.
//
// A converter writes a bundle with Writer and proves its output with Check
// before it ships the bundle. Both come from later changes to this package.
//
// The package does not use a database, so a converter can use it on its own.
package bundle
