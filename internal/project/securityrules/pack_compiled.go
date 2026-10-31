package securityrules

// compiledRules cover Java, Kotlin, C#, Go (beyond its AST rules), C, C++
// and Rust.
var compiledRules = []packRule{
	// Java
	{&kindShell, "java", `(method_invocation object: (method_invocation name: (identifier) @g) name: (identifier) @f arguments: (argument_list . (_) @dynamic)
		(#eq? @g "getRuntime") (#eq? @f "exec")) @match`},
	{&kindShell, "java", `(object_creation_expression type: (type_identifier) @t arguments: (argument_list (string_literal) @c (_) @dynamic)
		(#eq? @t "ProcessBuilder") (#match? @c "^.(-c|/c|/C).$")) @match`},
	{&kindSQL, "java", `(method_invocation name: (identifier) @f arguments: (argument_list (_) @sql)
		(#any-of? @f "executeQuery" "executeUpdate" "execute" "executeLargeUpdate" "addBatch" "prepareStatement" "prepareCall" "createQuery" "createNativeQuery" "createSQLQuery" "query" "queryForObject" "queryForList" "queryForMap" "queryForRowSet" "update" "batchUpdate")) @match`},
	{&kindSQL, "java", `(variable_declarator value: (_) @sqlbuilt) @match`},
	{&kindSQL, "java", `(assignment_expression right: (_) @sqlbuilt) @match`},
	{&kindDeserialize, "java", `(object_creation_expression type: (type_identifier) @t (#any-of? @t "ObjectInputStream" "XMLDecoder")) @match`},
	{&kindDeserialize, "java", `(method_invocation name: (identifier) @f (#eq? @f "enableDefaultTyping")) @match`},
	{&kindTLS, "java", `(method_declaration name: (identifier) @n body: (block) @b
		(#any-of? @n "checkServerTrusted" "checkClientTrusted") (#match? @b "^[{][[:space:]]*[}]$")) @match`},
	{&kindTLS, "java", `(method_declaration name: (identifier) @n parameters: (formal_parameters (formal_parameter type: (type_identifier) @p))
		body: (block . (return_statement (true)) .) (#eq? @n "verify") (#eq? @p "SSLSession")) @match`},
	{&kindTLS, "java", `(method_invocation name: (identifier) @f arguments: (argument_list (lambda_expression body: (true)))
		(#any-of? @f "setHostnameVerifier" "setDefaultHostnameVerifier" "hostnameVerifier")) @match`},
	{&kindTLS, "java", `([(type_identifier) (identifier)] @match (#any-of? @match "NoopHostnameVerifier" "TrustAllStrategy" "ALLOW_ALL_HOSTNAME_VERIFIER")
		(#not-has-ancestor? @match import_declaration))`},
	{&kindWeakHash, "java", `(method_invocation object: (identifier) @o name: (identifier) @f arguments: (argument_list . (string_literal) @alg)
		(#eq? @o "MessageDigest") (#eq? @f "getInstance") (#match? @alg "(?i)^.(md2|md4|md5|sha-?1|sha).$")) @match`},
	{&kindWeakCipher, "java", `(method_invocation object: (identifier) @o name: (identifier) @f arguments: (argument_list . (string_literal) @alg)
		(#eq? @o "Cipher") (#eq? @f "getInstance") (#match? @alg "(?i)^.(des|desede|rc2|rc4|arcfour|blowfish)([/].*)?.$|^.aes.$|/ecb/")) @match`},
	{&kindSecret, "java", `(variable_declarator name: (identifier) @name value: (string_literal) @secret) @match`},
	{&kindSecret, "java", `(assignment_expression left: [(identifier) (field_access)] @name right: (string_literal) @secret) @match`},

	// Kotlin
	{&kindShell, "kotlin", `(call_expression (navigation_expression (call_expression (navigation_expression (navigation_suffix (simple_identifier) @g)))
		(navigation_suffix (simple_identifier) @f)) (call_suffix (value_arguments . (value_argument (_) @dynamic)))
		(#eq? @g "getRuntime") (#eq? @f "exec")) @match`},
	{&kindSQL, "kotlin", `(call_expression (navigation_expression (navigation_suffix (simple_identifier) @f)) (call_suffix (value_arguments (value_argument (_) @sql)))
		(#any-of? @f "executeQuery" "executeUpdate" "execute" "addBatch" "prepareStatement" "createQuery" "createNativeQuery" "query" "queryForObject" "queryForList" "update" "rawQuery" "execSQL")) @match`},
	{&kindSQL, "kotlin", `(property_declaration (variable_declaration) (_) @sqlbuilt) @match`},
	{&kindWeakHash, "kotlin", `(call_expression (navigation_expression (simple_identifier) @o (navigation_suffix (simple_identifier) @f))
		(call_suffix (value_arguments . (value_argument (string_literal) @alg)))
		(#eq? @o "MessageDigest") (#eq? @f "getInstance") (#match? @alg "(?i)^.(md2|md4|md5|sha-?1|sha).$")) @match`},
	{&kindSecret, "kotlin", `(property_declaration (variable_declaration (simple_identifier) @name) (string_literal) @secret) @match`},

	// C#
	{&kindShell, "c_sharp", `(invocation_expression function: (member_access_expression expression: (identifier) @o name: (identifier) @f)
		arguments: (argument_list . (argument (_) @dynamic)) (#eq? @o "Process") (#eq? @f "Start")) @match`},
	{&kindShell, "c_sharp", `(object_creation_expression type: (identifier) @t arguments: (argument_list . (argument (_) @sh) (argument (_) @dynamic))
		(#eq? @t "ProcessStartInfo") (#match? @sh "(?i)^.(cmd|sh|bash|zsh|powershell|pwsh)([.]exe)?.$")) @match`},
	{&kindEval, "c_sharp", `(invocation_expression function: (member_access_expression expression: (identifier) @o name: (identifier) @f)
		arguments: (argument_list . (argument (_) @dynamic)) (#eq? @o "CSharpScript") (#any-of? @f "EvaluateAsync" "RunAsync" "Create")) @match`},
	{&kindSQL, "c_sharp", `(object_creation_expression type: (identifier) @t arguments: (argument_list (argument (_) @sql))
		(#any-of? @t "SqlCommand" "OleDbCommand" "OdbcCommand" "NpgsqlCommand" "MySqlCommand" "SqliteCommand" "SQLiteCommand" "OracleCommand" "SqlDataAdapter")) @match`},
	{&kindSQL, "c_sharp", `(assignment_expression left: [(identifier) (member_access_expression)] @p right: (_) @sql (#match? @p "CommandText$")) @match`},
	{&kindSQL, "c_sharp", `(invocation_expression function: (member_access_expression name: (identifier) @f) arguments: (argument_list (argument (_) @sql))
		(#any-of? @f "FromSqlRaw" "ExecuteSqlRaw" "ExecuteSqlRawAsync" "ExecuteSqlCommand" "ExecuteSqlCommandAsync" "SqlQueryRaw" "Query" "QueryAsync" "QueryFirst" "QueryFirstAsync" "QueryFirstOrDefault" "QueryFirstOrDefaultAsync" "QuerySingle" "QuerySingleAsync" "QueryMultiple" "Execute" "ExecuteAsync" "ExecuteScalar" "ExecuteScalarAsync" "ExecuteReader" "ExecuteReaderAsync")) @match`},
	{&kindSQL, "c_sharp", `(variable_declarator (_) @sqlbuilt) @match`},
	{&kindSQL, "c_sharp", `(assignment_expression left: (_) @p right: (_) @sqlbuilt (#not-match? @p "CommandText$")) @match`},
	{&kindDeserialize, "c_sharp", `(object_creation_expression type: (identifier) @t
		(#any-of? @t "BinaryFormatter" "SoapFormatter" "NetDataContractSerializer" "LosFormatter" "ObjectStateFormatter")) @match`},
	{&kindDeserialize, "c_sharp", `(member_access_expression expression: (identifier) @o name: (identifier) @n
		(#eq? @o "TypeNameHandling") (#any-of? @n "All" "Auto" "Objects" "Arrays")) @match`},
	{&kindTLS, "c_sharp", `(assignment_expression left: [(identifier) (member_access_expression)] @p right: (lambda_expression body: (boolean_literal) @b)
		(#match? @p "ValidationCallback$") (#eq? @b "true")) @match`},
	{&kindTLS, "c_sharp", `(assignment_expression left: [(identifier) (member_access_expression)] @p right: (lambda_expression body: (block . (return_statement (boolean_literal) @b) .))
		(#match? @p "ValidationCallback$") (#eq? @b "true")) @match`},
	{&kindTLS, "c_sharp", `(member_access_expression name: (identifier) @n (#eq? @n "DangerousAcceptAnyServerCertificateValidator")) @match`},
	{&kindWeakHash, "c_sharp", `(invocation_expression function: (member_access_expression expression: (identifier) @o name: (identifier) @f)
		(#any-of? @o "MD5" "SHA1") (#any-of? @f "Create" "HashData" "HashDataAsync")) @match`},
	{&kindWeakHash, "c_sharp", `(object_creation_expression type: (identifier) @t
		(#any-of? @t "MD5CryptoServiceProvider" "SHA1CryptoServiceProvider" "SHA1Managed" "MD5Cng" "SHA1Cng")) @match`},
	{&kindWeakCipher, "c_sharp", `(object_creation_expression type: (identifier) @t
		(#any-of? @t "DESCryptoServiceProvider" "TripleDESCryptoServiceProvider" "RC2CryptoServiceProvider")) @match`},
	{&kindWeakCipher, "c_sharp", `(invocation_expression function: (member_access_expression expression: (identifier) @o name: (identifier) @f)
		(#any-of? @o "DES" "TripleDES" "RC2") (#eq? @f "Create")) @match`},
	{&kindWeakCipher, "c_sharp", `(member_access_expression expression: (identifier) @o name: (identifier) @n (#eq? @o "CipherMode") (#eq? @n "ECB")) @match`},
	{&kindSecret, "c_sharp", `(variable_declarator name: (identifier) @name (string_literal) @secret) @match`},
	{&kindSecret, "c_sharp", `(assignment_expression left: [(identifier) (member_access_expression)] @name right: (string_literal) @secret) @match`},

	// Go: what go/ast rules do not cover.
	{&kindSQL, "go", `(call_expression function: (selector_expression field: (field_identifier) @f) arguments: (argument_list (_) @sql)
		(#any-of? @f "Query" "QueryContext" "QueryRow" "QueryRowContext" "Exec" "ExecContext" "Prepare" "PrepareContext" "Raw" "Select" "Get" "MustExec" "Queryx" "QueryRowx" "NamedExec" "NamedQuery")) @match`},
	{&kindSQL, "go", `(short_var_declaration right: (expression_list (_) @sqlbuilt)) @match`},
	{&kindSQL, "go", `(assignment_statement right: (expression_list (_) @sqlbuilt)) @match`},
	{&kindWeakHash, "go", `(call_expression function: (selector_expression operand: (identifier) @p field: (field_identifier) @f)
		(#any-of? @p "md5" "sha1" "md4") (#any-of? @f "New" "Sum")) @match`},
	{&kindWeakCipher, "go", `(call_expression function: (selector_expression operand: (identifier) @p field: (field_identifier) @f)
		(#any-of? @p "des" "rc4" "blowfish") (#any-of? @f "NewCipher" "NewTripleDESCipher")) @match`},
	{&kindSecret, "go", `(short_var_declaration left: (expression_list . (identifier) @name .) right: (expression_list . (_) @secret .)) @match`},
	{&kindSecret, "go", `(assignment_statement left: (expression_list . (_) @name .) right: (expression_list . (_) @secret .)) @match`},
	{&kindSecret, "go", `(const_spec name: (identifier) @name value: (expression_list . (_) @secret .)) @match`},
	{&kindSecret, "go", `(var_spec name: (identifier) @name value: (expression_list . (_) @secret .)) @match`},
	{&kindSecret, "go", `(keyed_element . (literal_element (identifier) @name) (literal_element (_) @secret) .) @match`},

	// C and C++
	{&kindGets, "c cpp", `(call_expression function: (identifier) @f (#eq? @f "gets")) @match`},
	{&kindUnboundedCopy, "c cpp", `(call_expression function: (identifier) @f arguments: (argument_list . (_) (_) @dynamic)
		(#any-of? @f "strcpy" "strcat" "wcscpy" "wcscat" "lstrcpy" "lstrcat" "lstrcpyA" "lstrcatA")) @match`},
	{&kindUnboundedCopy, "c cpp", `(call_expression function: (identifier) @f arguments: (argument_list . (_) . (string_literal) @format)
		(#any-of? @f "sprintf" "vsprintf" "swprintf") (#match? @format "%[-+ #0]*[0-9]*s")) @match`},
	{&kindShell, "c cpp", `(call_expression function: (identifier) @f arguments: (argument_list . (_) @dynamic)
		(#any-of? @f "system" "popen" "_popen" "_wsystem" "_wpopen")) @match`},
	{&kindShell, "cpp", `(call_expression function: (qualified_identifier) @f arguments: (argument_list . (_) @dynamic)
		(#match? @f "^(::|std::)(system|popen)$")) @match`},
	{&kindFormatString, "c cpp", `(call_expression function: (identifier) @f arguments: (argument_list . (_) @dynamic) (#any-of? @f "printf" "vprintf")) @match`},
	{&kindFormatString, "c cpp", `(call_expression function: (identifier) @f arguments: (argument_list . (_) . (_) @dynamic)
		(#any-of? @f "fprintf" "vfprintf" "syslog" "dprintf")) @match`},
	{&kindSecret, "c cpp", `(init_declarator declarator: (_) @name value: (string_literal) @secret) @match`},

	// Rust
	{&kindShell, "rust", `(call_expression function: (field_expression value: (call_expression function: (field_expression
		value: (call_expression function: (scoped_identifier name: (identifier) @n) arguments: (arguments . (string_literal) @sh))
		field: (field_identifier) @a1) arguments: (arguments . (string_literal) @c)) field: (field_identifier) @a2) arguments: (arguments . (_) @dynamic)
		(#eq? @n "new") (#match? @sh "^.(sh|bash|zsh|cmd|powershell|pwsh)([.]exe)?.$") (#eq? @a1 "arg") (#match? @c "^.(-c|/c|/C|-Command).$") (#eq? @a2 "arg")) @match`},
	{&kindTLS, "rust", `(call_expression function: (field_expression field: (field_identifier) @f) arguments: (arguments . (boolean_literal) @b)
		(#any-of? @f "danger_accept_invalid_certs" "danger_accept_invalid_hostnames") (#eq? @b "true")) @match`},
	{&kindSQL, "rust", `(call_expression function: [(identifier) (scoped_identifier) (field_expression)] @f arguments: (arguments (_) @sql)
		(#match? @f "(^|::|[.])(query|query_as|query_scalar|execute|batch_execute|prepare|query_row|query_map|sql_query)$")) @match`},
	{&kindWeakHash, "rust", `(call_expression function: (scoped_identifier path: (_) @p) (#match? @p "(^|::)(md5|Md5|md4|Md4|sha1|Sha1)$")) @match`},
	{&kindSecret, "rust", `(let_declaration pattern: (identifier) @name value: (string_literal) @secret) @match`},
	{&kindSecret, "rust", `(const_item name: (identifier) @name value: (string_literal) @secret) @match`},
	{&kindSecret, "rust", `(static_item name: (identifier) @name value: (string_literal) @secret) @match`},
}

var packRules = append(append([]packRule{}, scriptRules...), compiledRules...)
