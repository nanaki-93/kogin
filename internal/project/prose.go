package project

// TerseProse is the writing rule every model prompt shares: the reader is
// an engineer who reads the code next to the text, so each field says one
// thing, by name, and stops.
const TerseProse = "Write every text field tersely for an engineer who has the code open: " +
	"one short sentence per field (at most about 20 words) unless the field asks for more; " +
	"name the exact identifiers (functions, types, fields, flags, files) in backticks instead of describing them; " +
	"state facts and conditions plainly, without hedging (no 'may', 'might', 'it is unknown whether', 'this file cannot show'); " +
	"do not restate the title, the task or another field; no filler, no preamble, no Markdown headings. "
