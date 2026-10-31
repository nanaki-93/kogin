package securityrules

// packSamples hold risky and safe lines per language. A line ending in
// "<- rule ..." must be found by those rules; every other line must not be.
var packSamples = map[string]string{
	"app.py": `import subprocess, os, hashlib, pickle, requests, yaml, ssl
subprocess.run(cmd, shell=True)  # <- python.shell.dynamic-command
subprocess.run("ls -l", shell=True)
subprocess.run(["ls", path])
os.system("rm " + name)  # <- python.shell.dynamic-command
eval(data)  # <- python.eval.dynamic-code
eval("1 + 1")
cursor.execute(f"SELECT * FROM t WHERE id = {uid}")  # <- python.sql.string-built
cursor.execute("SELECT * FROM t WHERE id = %s" % uid)  # <- python.sql.string-built
cursor.execute("SELECT * FROM t WHERE id = {}".format(uid))  # <- python.sql.string-built
cursor.execute("SELECT * FROM t WHERE id = %s", (uid,))
runner.execute(job)
query = "SELECT * FROM t WHERE id = " + uid  # <- python.sql.string-built
query = "SELECT * FROM t WHERE id = ?"
db.execute(
	"SELECT p.id, title FROM post p"
	" WHERE p.id = ?",
	(post_id,),
)
requests.get(url, verify=False)  # <- python.tls.verification-disabled
requests.get(url, verify=True)
ctx = ssl._create_unverified_context()  # <- python.tls.verification-disabled
hashlib.md5(b)  # <- python.crypto.weak-hash
hashlib.md5(b, usedforsecurity=False)
hashlib.new("sha1")  # <- python.crypto.weak-hash
hashlib.sha256(b)
pickle.loads(blob)  # <- python.deserialization.unsafe
copy = pickle.loads(pickle.dumps(value))
yaml.load(text)  # <- python.deserialization.unsafe
yaml.load(text, Loader=yaml.SafeLoader)
yaml.safe_load(text)
api_key = "sk_live_51HabcdefGHIJ"  # <- python.secret.hard-coded
password = "changeme"
password = os.environ["PASSWORD"]
token_type = "bearer-token-x"
client = Client(password="hunter2hunter2")  # <- python.secret.hard-coded
config = {"secret": "abcdefghijklmnop"}  # <- python.secret.hard-coded
`,
	"server.js": `const { exec } = require('child_process');
exec(cmd);  // <- javascript.shell.dynamic-command
exec('ls -l');
child_process.execSync(` + "`ls ${dir}`" + `);  // <- javascript.shell.dynamic-command
/ab+c/.exec(text);
spawn(cmd, args, { shell: true });  // <- javascript.shell.dynamic-command
spawn('ls', args);
eval(body);  // <- javascript.eval.dynamic-code
new Function('a', 'b', 'return a + b');
new Function(body);  // <- javascript.eval.dynamic-code
db.query("SELECT * FROM t WHERE id = " + id);  // <- javascript.sql.string-built
db.query(` + "`SELECT * FROM t WHERE id = ${id}`" + `);  // <- javascript.sql.string-built
db.query("SELECT * FROM t WHERE id = $1", [id]);
const q = "SELECT * FROM t WHERE id = " + id;  // <- javascript.sql.string-built
const fixed = "SELECT * FROM t " + "WHERE id = 1";
db.query("SELECT * FROM t " + "WHERE id = 1");
sql` + "`SELECT * FROM t WHERE id = ${id}`" + `;
cache.get(key);
const agent = new https.Agent({ rejectUnauthorized: false });  // <- javascript.tls.verification-disabled
const safe = new https.Agent({ rejectUnauthorized: true });
process.env.NODE_TLS_REJECT_UNAUTHORIZED = "0";  // <- javascript.tls.verification-disabled
crypto.createHash('md5');  // <- javascript.crypto.weak-hash
crypto.createHash('sha256');
const apiKey = 'AKIAabcdefghijkl';  // <- javascript.secret.hard-coded
const password = process.env.PASSWORD;
const label = 'password-field';
const options = { password: 'hunter2hunter2' };  // <- javascript.secret.hard-coded
`,
	"client.ts": `class Api {
	private githubToken = "ghp_abcdefghijklmnop";  // <- typescript.secret.hard-coded
	private token = "comment.doc.tag";
	run(cmd: string) {
		exec(cmd);  // <- typescript.shell.dynamic-command
		db.query(` + "`DELETE FROM t WHERE id = ${this.id}`" + `);  // <- typescript.sql.string-built
	}
}
`,
	"Repo.java": `class Repo {
	void f(String cmd, String id) throws Exception {
		Runtime.getRuntime().exec(cmd);  // <- java.shell.dynamic-command
		Runtime.getRuntime().exec("ls");
		new ProcessBuilder("sh", "-c", cmd);  // <- java.shell.dynamic-command
		new ProcessBuilder("ls", cmd);
		stmt.executeQuery("SELECT * FROM t WHERE id = " + id);  // <- java.sql.string-built
		em.createQuery(String.format("SELECT a FROM T a WHERE id = %s", id));  // <- java.sql.string-built
		stmt.executeQuery("SELECT * FROM t WHERE id = ?");
		String q = "SELECT * FROM t WHERE id = " + id;  // <- java.sql.string-built
		ObjectInputStream in = new ObjectInputStream(s);  // <- java.deserialization.unsafe
		conn.setHostnameVerifier((h, s) -> true);  // <- java.tls.verification-disabled
		builder.setSSLHostnameVerifier(NoopHostnameVerifier.INSTANCE);  // <- java.tls.verification-disabled
		MessageDigest.getInstance("MD5");  // <- java.crypto.weak-hash
		MessageDigest.getInstance("SHA-256");
		Cipher.getInstance("DES/CBC/PKCS5Padding");  // <- java.crypto.weak-cipher
		Cipher.getInstance("AES");  // <- java.crypto.weak-cipher
		Cipher.getInstance("AES/GCM/NoPadding");
		String password = "hunter2hunter2";  // <- java.secret.hard-coded
		String user = "administrator";
	}
	public void checkServerTrusted(X509Certificate[] c, String a) {}  // <- java.tls.verification-disabled
	public boolean verify(String host, SSLSession session) { return true; }  // <- java.tls.verification-disabled
}
`,
	"Main.kt": `fun f(cmd: String, id: String) {
	Runtime.getRuntime().exec(cmd)  // <- kotlin.shell.dynamic-command
	stmt.executeQuery("SELECT * FROM t WHERE id = " + id)  // <- kotlin.sql.string-built
	stmt.executeQuery("SELECT * FROM t WHERE id = $id")  // <- kotlin.sql.string-built
	stmt.executeQuery("SELECT * FROM t WHERE id = ?")
	val q = "SELECT * FROM t WHERE id = $id"  // <- kotlin.sql.string-built
	val password = "hunter2hunter2"  // <- kotlin.secret.hard-coded
	MessageDigest.getInstance("MD5")  // <- kotlin.crypto.weak-hash
}
`,
	"app.rb": `system("ls #{dir}")  # <- ruby.shell.dynamic-command
system("ls", dir)
system("ls -l")
out = ` + "`ls #{dir}`" + `  # <- ruby.shell.dynamic-command
out = ` + "`ls -l`" + `
eval(code)  # <- ruby.eval.dynamic-code
instance_eval(&block)
User.where("name = '#{name}'")  # <- ruby.sql.interpolated-fragment
User.where("name = ?", name)
User.where(name: name)
sql = "SELECT * FROM users WHERE name = '#{name}'"  # <- ruby.sql.string-built
get "forgot_password" => "password_resets#forgot_password"
User.find_by_sql("SELECT * FROM users WHERE id = " + id)  # <- ruby.sql.string-built
Marshal.load(data)  # <- ruby.deserialization.unsafe
http.verify_mode = OpenSSL::SSL::VERIFY_NONE  # <- ruby.tls.verification-disabled
http.verify_mode = OpenSSL::SSL::VERIFY_PEER
conn = Faraday.new(url, ssl: { verify: false })  # <- ruby.tls.verification-disabled
Digest::MD5.hexdigest(x)  # <- ruby.crypto.weak-hash
OpenSSL::Digest::SHA1.new  # <- ruby.crypto.weak-hash
Digest::SHA256.hexdigest(x)
password = "hunter2hunter2"  # <- ruby.secret.hard-coded
@api_key = 'abcdefghijklmnop'  # <- ruby.secret.hard-coded
password = ENV["PASSWORD"]
`,
	"index.php": `<?php
exec("ls " . $dir);  // <- php.shell.dynamic-command
exec("ls -l");
shell_exec($cmd);  // <- php.shell.dynamic-command
$o = ` + "`ls $dir`" + `;  // <- php.shell.dynamic-command
eval($code);  // <- php.eval.dynamic-code
mysqli_query($conn, "SELECT * FROM t WHERE id = $id");  // <- php.sql.string-built
$pdo->query("SELECT * FROM t WHERE id = " . $id);  // <- php.sql.string-built
$pdo->prepare("SELECT * FROM t WHERE id = ?");
$query = "SELECT first_name FROM users WHERE user_id = '$id';";  // <- php.sql.string-built
DB::select("SELECT * FROM t WHERE id = {$id}");  // <- php.sql.string-built
unserialize($data);  // <- php.deserialization.unsafe
unserialize($data, ['allowed_classes' => false]);
curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);  // <- php.tls.verification-disabled
curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, 0);  // <- php.tls.verification-disabled
curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
$client = new Client(['verify' => false]);  // <- php.tls.verification-disabled
md5($password);  // <- php.crypto.weak-hash
hash('sha1', $data);  // <- php.crypto.weak-hash
hash('sha256', $data);
$password = "hunter2hunter2";  // <- php.secret.hard-coded
define('DB_PASSWORD', 'hunter2hunter2');  // <- php.secret.hard-coded
$t = "plain text value";
`,
	"Data.cs": `class Data {
	void F(string cmd, string id) {
		Process.Start(cmd);  // <- csharp.shell.dynamic-command
		Process.Start("notepad.exe");
		var si = new ProcessStartInfo("cmd.exe", "/c " + cmd);  // <- csharp.shell.dynamic-command
		var c = new SqlCommand("SELECT * FROM t WHERE id = " + id, conn);  // <- csharp.sql.string-built
		cmd.CommandText = $"SELECT * FROM t WHERE id = {id}";  // <- csharp.sql.string-built
		cmd.CommandText = "SELECT * FROM t WHERE id = @id";
		var q = "SELECT * FROM t WHERE id = " + id;  // <- csharp.sql.string-built
		ctx.Users.FromSqlRaw("SELECT * FROM u WHERE id = " + id);  // <- csharp.sql.string-built
		ctx.Users.FromSqlInterpolated($"SELECT * FROM u WHERE id = {id}");
		var f = new BinaryFormatter();  // <- csharp.deserialization.unsafe
		var s = new JsonSerializerSettings { TypeNameHandling = TypeNameHandling.All };  // <- csharp.deserialization.unsafe
		ServicePointManager.ServerCertificateValidationCallback = (a, b, c, d) => true;  // <- csharp.tls.verification-disabled
		handler.ServerCertificateCustomValidationCallback = HttpClientHandler.DangerousAcceptAnyServerCertificateValidator;  // <- csharp.tls.verification-disabled
		var h = MD5.Create();  // <- csharp.crypto.weak-hash
		var k = SHA256.Create();
		var d = DES.Create();  // <- csharp.crypto.weak-cipher
		string password = "hunter2hunter2";  // <- csharp.secret.hard-coded
		var p = "plain text";
	}
}
`,
	"store.go": `package store

import (
	"crypto/md5"
	"fmt"
)

func f(db DB, id string) {
	db.Query("SELECT * FROM t WHERE id = " + id)  // <- go.sql.string-built
	db.Exec(fmt.Sprintf("DELETE FROM t WHERE id = %s", id))  // <- go.sql.string-built
	db.Query("SELECT * FROM t WHERE id = $1", id)
	q := "SELECT * FROM t WHERE id = " + id  // <- go.sql.string-built
	h := md5.New()  // <- go.crypto.weak-hash
	password := "hunter2hunter2"  // <- go.secret.hard-coded
	cfg := Config{Password: "hunter2hunter2"}  // <- go.secret.hard-coded
	name := "administrator"
	_, _, _, _, _ = h, password, cfg, name, q
}

const apiKey = "abcdefghijklmnop"  // <- go.secret.hard-coded
`,
	"copy.c": `#include <stdio.h>
void f(char *a, const char *fmt) {
	char buf[10];
	gets(buf);  // <- c.memory.gets
	strcpy(buf, a);  // <- c.memory.unbounded-copy
	strcpy(buf, "ok");
	sprintf(buf, "%s", a);  // <- c.memory.unbounded-copy
	sprintf(buf, "%d", 1);
	system(a);  // <- c.shell.dynamic-command
	system("ls");
	printf(a);  // <- c.format.dynamic-format-string
	printf("%s", a);
	fprintf(stderr, fmt);  // <- c.format.dynamic-format-string
	const char *password = "hunter2hunter2";  // <- c.secret.hard-coded
}
`,
	"run.cpp": `void f(const std::string &cmd) {
	std::system(cmd.c_str());  // <- cpp.shell.dynamic-command
	std::system("ls");
}
`,
	"main.rs": `fn f(cmd: &str, id: &str) {
	Command::new("sh").arg("-c").arg(cmd).output();  // <- rust.shell.dynamic-command
	Command::new("ls").arg("-l").arg(cmd).output();
	let c = reqwest::Client::builder().danger_accept_invalid_certs(true).build();  // <- rust.tls.verification-disabled
	sqlx::query(&format!("SELECT * FROM t WHERE id = {}", id));  // <- rust.sql.string-built
	sqlx::query("SELECT * FROM t WHERE id = $1");
	let h = md5::compute(b);  // <- rust.crypto.weak-hash
	let password = "hunter2hunter2";  // <- rust.secret.hard-coded
}
const API_KEY: &str = "abcdefghijklmnop";  // <- rust.secret.hard-coded
`,
}
