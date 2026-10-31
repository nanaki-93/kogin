package securityrules

// scriptRules cover Python, JavaScript and TypeScript, Ruby and PHP.
var scriptRules = []packRule{
	// Python
	{&kindShell, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f)
		arguments: (argument_list . (_) @dynamic (keyword_argument name: (identifier) @k value: (true)))
		(#eq? @m "subprocess") (#any-of? @f "run" "call" "check_call" "check_output" "Popen") (#eq? @k "shell")) @match`},
	{&kindShell, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) arguments: (argument_list . (_) @dynamic)
		(#any-of? @m "os" "subprocess" "commands") (#any-of? @f "system" "popen" "getoutput" "getstatusoutput")) @match`},
	{&kindEval, "python", `(call function: (identifier) @f arguments: (argument_list . (_) @dynamic) (#any-of? @f "eval" "exec")) @match`},
	{&kindSQL, "python", `(call function: (attribute attribute: (identifier) @f) arguments: (argument_list (_) @sql)
		(#any-of? @f "execute" "executemany" "executescript" "raw" "mogrify" "read_sql" "read_sql_query")) @match`},
	{&kindSQL, "python", `(call function: (identifier) @f arguments: (argument_list . (_) @sql) (#eq? @f "text")) @match`},
	{&kindSQL, "python", `(assignment right: (_) @sqlbuilt) @match`},
	{&kindTLS, "python", `(call function: (attribute attribute: (identifier) @f) arguments: (argument_list (keyword_argument name: (identifier) @k value: (false)))
		(#any-of? @f "get" "post" "put" "patch" "delete" "head" "options" "request" "stream" "Client" "AsyncClient" "Session") (#eq? @k "verify")) @match`},
	{&kindTLS, "python", `(attribute object: (identifier) @m attribute: (identifier) @a (#eq? @m "ssl") (#any-of? @a "_create_unverified_context" "CERT_NONE")) @match`},
	{&kindWeakHash, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) arguments: (argument_list) @args
		(#eq? @m "hashlib") (#any-of? @f "md5" "sha1") (#not-match? @args "usedforsecurity")) @match`},
	{&kindWeakHash, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) arguments: (argument_list . (string) @alg) @args
		(#eq? @m "hashlib") (#eq? @f "new") (#match? @alg "(?i)^.(md5|sha1|md4).$") (#not-match? @args "usedforsecurity")) @match`},
	{&kindDeserialize, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) arguments: (argument_list) @args
		(#any-of? @m "pickle" "cPickle" "dill" "marshal" "jsonpickle") (#any-of? @f "load" "loads" "decode" "Unpickler") (#not-match? @args "^[(][a-zA-Z_.]*dumps[(]")) @match`},
	{&kindDeserialize, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) arguments: (argument_list) @args
		(#eq? @m "yaml") (#any-of? @f "load" "load_all") (#not-match? @args "SafeLoader|BaseLoader")) @match`},
	{&kindDeserialize, "python", `(call function: (attribute object: (identifier) @m attribute: (identifier) @f) (#eq? @m "yaml") (#any-of? @f "unsafe_load" "unsafe_load_all")) @match`},
	{&kindSecret, "python", `(assignment left: [(identifier) (attribute)] @name right: (string) @secret) @match`},
	{&kindSecret, "python", `(keyword_argument name: (identifier) @name value: (string) @secret) @match`},
	{&kindSecret, "python", `(pair key: (string) @name value: (string) @secret) @match`},

	// JavaScript, TypeScript and TSX
	{&kindShell, "javascript typescript tsx", `(call_expression function: (identifier) @f arguments: (arguments . (_) @dynamic) (#any-of? @f "exec" "execSync")) @match`},
	{&kindShell, "javascript typescript tsx", `(call_expression function: (member_expression object: (identifier) @m property: (property_identifier) @f) arguments: (arguments . (_) @dynamic)
		(#any-of? @m "cp" "child" "childProcess" "child_process") (#any-of? @f "exec" "execSync")) @match`},
	{&kindShell, "javascript typescript tsx", `(call_expression function: [(identifier) @f (member_expression property: (property_identifier) @f)]
		arguments: (arguments . (_) @dynamic (object (pair key: (property_identifier) @k value: (true))))
		(#any-of? @f "spawn" "spawnSync" "execFile" "execFileSync") (#eq? @k "shell")) @match`},
	{&kindEval, "javascript typescript tsx", `(call_expression function: (identifier) @f arguments: (arguments . (_) @dynamic) (#eq? @f "eval")) @match`},
	{&kindEval, "javascript typescript tsx", `(new_expression constructor: (identifier) @f arguments: (arguments (_) @dynamic) (#eq? @f "Function")) @match`},
	{&kindEval, "javascript typescript tsx", `(call_expression function: (member_expression object: (identifier) @m property: (property_identifier) @f) arguments: (arguments . (_) @dynamic)
		(#eq? @m "vm") (#any-of? @f "runInThisContext" "runInNewContext" "runInContext" "compileFunction")) @match`},
	{&kindSQL, "javascript typescript tsx", `(call_expression function: (member_expression property: (property_identifier) @f) arguments: (arguments (_) @sql)
		(#any-of? @f "query" "execute" "exec" "raw" "run" "all" "get" "prepare" "each" "$queryRawUnsafe" "$executeRawUnsafe" "whereRaw" "orderByRaw" "havingRaw" "joinRaw" "selectRaw" "fromRaw")) @match`},
	{&kindSQL, "javascript typescript tsx", `(variable_declarator value: (_) @sqlbuilt) @match`},
	{&kindSQL, "javascript typescript tsx", `(assignment_expression right: (_) @sqlbuilt) @match`},
	{&kindTLS, "javascript typescript tsx", `(pair key: [(property_identifier) (string)] @k value: (false) (#match? @k "^.?rejectUnauthorized.?$")) @match`},
	{&kindTLS, "javascript typescript tsx", `(assignment_expression left: (member_expression property: (property_identifier) @p) right: (string) @v
		(#eq? @p "NODE_TLS_REJECT_UNAUTHORIZED") (#match? @v "^.0.$")) @match`},
	{&kindWeakHash, "javascript typescript tsx", `(call_expression function: (member_expression property: (property_identifier) @f) arguments: (arguments . (string) @alg)
		(#eq? @f "createHash") (#match? @alg "(?i)^.(md5|md4|sha1).$")) @match`},
	{&kindDeserialize, "javascript typescript tsx", `(call_expression function: [(identifier) (member_expression property: (property_identifier))] @f arguments: (arguments . (_) @dynamic)
		(#match? @f "(^|[.])unserialize$")) @match`},
	{&kindSecret, "javascript typescript tsx", `(variable_declarator name: (identifier) @name value: (string) @secret) @match`},
	{&kindSecret, "javascript typescript tsx", `(pair key: [(property_identifier) (string)] @name value: (string) @secret) @match`},
	{&kindSecret, "javascript typescript tsx", `(assignment_expression left: [(identifier) (member_expression)] @name right: (string) @secret) @match`},
	{&kindSecret, "typescript tsx", `(public_field_definition name: (property_identifier) @name value: (string) @secret) @match`},

	// Ruby
	{&kindShell, "ruby", `(call method: (identifier) @f arguments: (argument_list . (_) @dynamic .)
		(#any-of? @f "system" "exec" "spawn" "popen" "popen2" "popen2e" "popen3" "capture2" "capture2e" "capture3" "pipeline_r" "pipeline_w")) @match`},
	{&kindShell, "ruby", `(subshell) @dynamic @match`},
	{&kindEval, "ruby", `(call method: (identifier) @f arguments: (argument_list . (_) @dynamic) (#any-of? @f "eval" "instance_eval" "class_eval" "module_eval")) @match`},
	{&kindSQL, "ruby", `(call method: (identifier) @f arguments: (argument_list (_) @sql)
		(#any-of? @f "find_by_sql" "count_by_sql" "execute" "exec_query" "select_all" "select_one" "select_value" "select_values" "select_rows")) @match`},
	{&kindSQLFragment, "ruby", `(call method: (identifier) @f arguments: (argument_list . (_) @built)
		(#any-of? @f "where" "not" "order" "reorder" "group" "having" "joins" "from" "lock" "pluck" "calculate" "update_all" "delete_all" "destroy_by" "delete_by" "find_by")) @match`},
	{&kindSQL, "ruby", `(assignment right: (_) @sqlbuilt) @match`},
	{&kindTLS, "ruby", `(scope_resolution name: (constant) @c (#eq? @c "VERIFY_NONE")) @match`},
	{&kindTLS, "ruby", `(pair key: [(hash_key_symbol) (simple_symbol) (string)] @k value: (false)
		(#match? @k "^:?.?(verify|verify_ssl|ssl_verify|ssl_verify_peer|verify_peer).?$")) @match`},
	{&kindWeakHash, "ruby", `(scope_resolution scope: (_) @s name: (constant) @n (#match? @s "Digest$") (#any-of? @n "MD5" "SHA1" "RMD160")) @match`},
	{&kindDeserialize, "ruby", `(call receiver: (constant) @r method: (identifier) @f (#eq? @r "Marshal") (#any-of? @f "load" "restore")) @match`},
	{&kindDeserialize, "ruby", `(call receiver: (constant) @r method: (identifier) @f (#any-of? @r "YAML" "Psych") (#any-of? @f "unsafe_load" "unsafe_load_file")) @match`},
	{&kindSecret, "ruby", `(assignment left: [(identifier) (constant) (instance_variable) (class_variable) (global_variable)] @name right: (string) @secret) @match`},
	{&kindSecret, "ruby", `(pair key: [(hash_key_symbol) (simple_symbol) (string)] @name value: (string) @secret) @match`},

	// PHP
	{&kindShell, "php", `(function_call_expression function: (name) @f arguments: (arguments . (argument (_) @dynamic))
		(#any-of? @f "exec" "shell_exec" "system" "passthru" "popen" "proc_open" "pcntl_exec")) @match`},
	{&kindShell, "php", `(shell_command_expression) @dynamic @match`},
	{&kindEval, "php", `(function_call_expression function: (name) @f arguments: (arguments . (argument (_) @dynamic)) (#any-of? @f "eval" "create_function")) @match`},
	{&kindSQL, "php", `(function_call_expression function: (name) @f arguments: (arguments (argument (_) @sql))
		(#any-of? @f "mysqli_query" "mysql_query" "mysqli_multi_query" "mysqli_real_query" "pg_query" "sqlite_query" "sqlsrv_query" "odbc_exec" "db2_exec" "oci_parse")) @match`},
	{&kindSQL, "php", `(member_call_expression name: (name) @f arguments: (arguments (argument (_) @sql))
		(#any-of? @f "query" "exec" "prepare" "multi_query" "real_query" "execute" "rawQuery" "select" "statement" "unprepared" "whereRaw" "selectRaw" "orderByRaw" "havingRaw" "fromRaw")) @match`},
	{&kindSQL, "php", `(scoped_call_expression name: (name) @f arguments: (arguments (argument (_) @sql))
		(#any-of? @f "select" "statement" "unprepared" "raw" "insert" "update" "delete")) @match`},
	{&kindSQL, "php", `(assignment_expression right: (_) @sqlbuilt) @match`},
	{&kindDeserialize, "php", `(function_call_expression function: (name) @f arguments: (arguments . (argument (_) @dynamic)) @args
		(#eq? @f "unserialize") (#not-match? @args "allowed_classes")) @match`},
	{&kindTLS, "php", `(function_call_expression function: (name) @f arguments: (arguments (argument (name) @opt) (argument [(boolean) (integer)] @v))
		(#eq? @f "curl_setopt") (#any-of? @opt "CURLOPT_SSL_VERIFYPEER" "CURLOPT_SSL_VERIFYHOST") (#match? @v "(?i)^(false|0)$")) @match`},
	{&kindTLS, "php", `(array_element_initializer [(string) (encapsed_string)] @k (boolean) @v
		(#match? @k "^.(verify|verify_peer|verify_peer_name).$") (#match? @v "(?i)^false$")) @match`},
	{&kindTLS, "php", `(array_element_initializer [(string) (encapsed_string)] @k (boolean) @v
		(#match? @k "^.allow_self_signed.$") (#match? @v "(?i)^true$")) @match`},
	{&kindWeakHash, "php", `(function_call_expression function: (name) @f (#any-of? @f "md5" "sha1" "md5_file" "sha1_file")) @match`},
	{&kindWeakHash, "php", `(function_call_expression function: (name) @f arguments: (arguments . (argument (_) @alg))
		(#any-of? @f "hash" "hash_file" "hash_init") (#match? @alg "(?i)^.(md5|md4|sha1).$")) @match`},
	{&kindSecret, "php", `(assignment_expression left: [(variable_name) (member_access_expression)] @name right: [(string) (encapsed_string)] @secret) @match`},
	{&kindSecret, "php", `(array_element_initializer [(string) (encapsed_string)] @name [(string) (encapsed_string)] @secret) @match`},
	{&kindSecret, "php", `(function_call_expression function: (name) @f arguments: (arguments . (argument (_) @name) (argument [(string) (encapsed_string)] @secret))
		(#eq? @f "define")) @match`},
	{&kindSecret, "php", `(const_element (name) @name [(string) (encapsed_string)] @secret) @match`},
}
