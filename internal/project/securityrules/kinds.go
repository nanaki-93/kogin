package securityrules

// The kinds of risk the rule packs find. Every language reports the same kind
// with the same words; the rule ID names the language.
var (
	kindShell = ruleKind{id: "shell.dynamic-command", category: "command-injection", title: "Command built from dynamic data",
		severity: "high", confidence: "medium", cwe: "CWE-78",
		observed:      "A command a shell or the operating system runs is built from a value that is not a fixed string literal.",
		preconditions: "Where the value comes from and whether the call is reachable are unknown; syntactic review is required.",
		remediation:   "Run the program with a fixed argument list and no shell, or check the value against an allow-list first.",
		verification:  "Trace the value to its sources and, in a test, pass one containing shell metacharacters such as ; or $( )."}
	kindEval = ruleKind{id: "eval.dynamic-code", category: "code-injection", title: "Code evaluated from dynamic data",
		severity: "high", confidence: "medium", cwe: "CWE-95",
		observed:      "Source code is evaluated from a value that is not a fixed string literal.",
		preconditions: "Where the value comes from and whether the call is reachable are unknown; syntactic review is required.",
		remediation:   "Replace evaluation with a parser or a lookup of allowed operations.",
		verification:  "Trace the value to its sources and confirm no caller can supply code."}
	kindSQL = ruleKind{id: "sql.string-built", category: "sql-injection", title: "SQL statement built from strings",
		severity: "high", confidence: "medium", cwe: "CWE-89",
		observed:      "An SQL statement passed to the database is assembled by concatenation, interpolation or formatting.",
		preconditions: "Whether the inserted values can come from outside the program is unknown; syntactic review is required.",
		remediation:   "Use bound parameters (placeholders) for values, and an allow-list for identifiers such as column names.",
		verification:  "Trace the inserted values and, in a test, pass one containing a quote to confirm it cannot change the statement."}
	kindSQLFragment = ruleKind{id: "sql.interpolated-fragment", category: "sql-injection", title: "SQL fragment built from strings",
		severity: "high", confidence: "medium", cwe: "CWE-89",
		observed:      "A raw SQL fragment given to the query builder is assembled by interpolation or concatenation.",
		preconditions: "Whether the inserted values can come from outside the program is unknown; syntactic review is required.",
		remediation:   "Pass values as bound parameters (the placeholder form of the call) or as a hash of conditions.",
		verification:  "Trace the inserted values and, in a test, pass one containing a quote to confirm it cannot change the query."}
	kindTLS = ruleKind{id: "tls.verification-disabled", category: "transport-security", title: "TLS certificate verification is disabled",
		severity: "high", confidence: "high", cwe: "CWE-295", skipTests: true,
		observed:      "A TLS client or server is configured to accept any certificate or host name.",
		preconditions: "Whether this configuration handles untrusted connections is unknown; syntactic review is required.",
		remediation:   "Keep verification on; trust a private certificate authority explicitly when a test or internal service needs one.",
		verification:  "Connect through the code path to a server with an invalid certificate and confirm the connection fails."}
	kindWeakHash = ruleKind{id: "crypto.weak-hash", category: "weak-cryptography", title: "Weak hash algorithm",
		severity: "low", confidence: "medium", cwe: "CWE-328", skipTests: true,
		observed:      "MD5, SHA-1 or a similar broken hash function is used.",
		preconditions: "Whether the digest protects passwords, signatures or integrity against an attacker is unknown; syntactic review is required.",
		remediation:   "Use SHA-256 or stronger for integrity, and a password hash (Argon2, bcrypt, scrypt) for passwords.",
		verification:  "Find what the digest is compared against or stored as, and whether an attacker controls the input."}
	kindWeakCipher = ruleKind{id: "crypto.weak-cipher", category: "weak-cryptography", title: "Weak cipher or cipher mode",
		severity: "medium", confidence: "medium", cwe: "CWE-327", skipTests: true,
		observed:      "DES, RC4, Blowfish or the ECB mode is used to encrypt.",
		preconditions: "What the encrypted data protects is unknown; syntactic review is required.",
		remediation:   "Use an authenticated cipher such as AES-GCM or ChaCha20-Poly1305.",
		verification:  "Find what the ciphertext protects and whether it leaves the process."}
	kindDeserialize = ruleKind{id: "deserialization.unsafe", category: "insecure-deserialization", title: "Unsafe deserialization",
		severity: "medium", confidence: "medium", cwe: "CWE-502", skipTests: true,
		observed:      "Data is deserialized with a format that can create arbitrary objects or run code.",
		preconditions: "Whether the data can come from outside the program is unknown; syntactic review is required.",
		remediation:   "Deserialize untrusted data only with a data-only format (JSON) or a safe loader limited to expected types.",
		verification:  "Trace where the serialized bytes come from and whether anyone else can write them."}
	kindSecret = ruleKind{id: "secret.hard-coded", category: "hard-coded-secret", title: "Hard-coded secret",
		severity: "medium", confidence: "medium", cwe: "CWE-798", skipTests: true,
		observed:      "A variable or key named like a password, token or key is set to a string literal in the source.",
		preconditions: "Whether the value is a real credential is unknown; syntactic review is required.",
		remediation:   "Read the value from the environment or a secret store, and rotate it if it was ever real.",
		verification:  "Check whether the value works against the service it names and whether it is in the repository history."}
	kindUnboundedCopy = ruleKind{id: "memory.unbounded-copy", category: "memory-safety", title: "Copy without a length bound",
		severity: "medium", confidence: "medium", cwe: "CWE-120",
		observed:      "A string is copied, appended or formatted into a buffer by a function that does not take the buffer's size.",
		preconditions: "Whether the source can be longer than the buffer is unknown; syntactic review is required.",
		remediation:   "Use a bounded function (snprintf, strlcpy, or std::string) and check the result for truncation.",
		verification:  "Find the buffer's size and the longest value that can reach the call."}
	kindGets = ruleKind{id: "memory.gets", category: "memory-safety", title: "Input read with gets",
		severity: "high", confidence: "high", cwe: "CWE-242",
		observed:      "gets reads a line into a buffer with no way to bound its length.",
		preconditions: "Whether the input is controlled by a user is unknown; syntactic review is required.",
		remediation:   "Use fgets with the buffer's size, or std::getline.",
		verification:  "Feed a line longer than the buffer in a test built with AddressSanitizer."}
	kindFormatString = ruleKind{id: "format.dynamic-format-string", category: "format-string", title: "Format string from dynamic data",
		severity: "medium", confidence: "medium", cwe: "CWE-134",
		observed:      "A printf-family function takes its format string from a value that is not a fixed literal.",
		preconditions: "Whether the value can contain % directives from outside the program is unknown; syntactic review is required.",
		remediation:   "Pass a fixed format such as \"%s\" and the value as its argument.",
		verification:  "Trace the value and, in a test, pass one containing %n or %s directives."}
)
