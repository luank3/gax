package bashcompress

import (
	"fmt"
	"strings"
	"testing"
)

const cargoTestPassFixture = `   Compiling gax v0.1.0 (/home/u/gax)
    Finished ` + "`test`" + ` profile [unoptimized + debuginfo] target(s) in 1.20s
     Running unittests src/main.rs (target/debug/deps/gax-1234)

running 15 tests
test utils::test_parse ... ok
test utils::test_format ... ok
test utils::test_render ... ok

test result: ok. 15 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.01s

     Running tests/integration.rs (target/debug/deps/integration-5678)

running 4 tests
test it_works ... ok

test result: ok. 4 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.20s
`

func TestFilterCargoTestCollapsesPassingTests(t *testing.T) {
	result, ok := Compress("cargo test", cargoTestPassFixture, 0)
	if !ok {
		t.Fatal("cargo test was not compacted")
	}
	want := "cargo test: 19 passed (2 suites, 0.21s)"
	if result.Text != want {
		t.Fatalf("cargo test = %q, want %q", result.Text, want)
	}
	if !result.Omitted {
		t.Fatal("collapsed passing tests were not reported")
	}
}

const cargoTestFailFixture = `   Compiling gax v0.1.0
    Finished test profile in 0.50s
     Running unittests src/main.rs (target/debug/deps/gax-1234)

running 15 tests
test utils::test_parse ... ok
test utils::test_edge_case ... FAILED
test utils::test_overflow ... FAILED

failures:

---- utils::test_edge_case stdout ----
thread 'utils::test_edge_case' panicked at src/utils.rs:18:
assertion failed: ` + "`left == right`" + `
  left: 1,
 right: 2,

---- utils::test_overflow stdout ----
thread 'utils::test_overflow' panicked at src/utils.rs:44:
attempt to subtract with overflow

failures:
    utils::test_edge_case
    utils::test_overflow

test result: FAILED. 13 passed; 2 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.30s
`

func TestFilterCargoTestKeepsFailureLocations(t *testing.T) {
	output := "running 3 tests\n" +
		"test utils::test_edge_case ... FAILED\n\n" +
		"failures:\n\n" +
		"---- utils::test_edge_case stdout ----\n" +
		"thread 'utils::test_edge_case' panicked at src/utils.rs:18:5:\n" +
		"assertion `left == right` failed\n" +
		"  left: 1,\n" +
		" right: 2,\n" +
		"note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n" +
		"thread backtrace line 1\nthread backtrace line 2\nthread backtrace line 3\n" +
		"thread backtrace line 4\nthread backtrace line 5\n\n" +
		"failures:\n    utils::test_edge_case\n\n" +
		"test result: FAILED. 2 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.10s\n"
	result, ok := Compress("cargo test", output, 101)
	if !ok {
		t.Fatal("a failing cargo test was not compacted")
	}
	if !strings.Contains(result.Text, "1. utils::test_edge_case") {
		t.Fatalf("cargo test lost the failing test's name: %q", result.Text)
	}
	if !strings.Contains(result.Text, "panicked at src/utils.rs:18:5") {
		t.Fatalf("cargo test lost the panic location: %q", result.Text)
	}
	if !strings.Contains(result.Text, "left: 1") || !strings.Contains(result.Text, "right: 2") {
		t.Fatalf("cargo test lost the compared values: %q", result.Text)
	}
	if !strings.Contains(result.Text, "… (") || !strings.Contains(result.Text, "more lines)") {
		t.Fatalf("cargo test did not say which lines it dropped: %q", result.Text)
	}
}

func TestFilterCargoTestLeavesCompileErrorsAlone(t *testing.T) {
	compileFailure := "   Compiling gax v0.1.0\nerror[E0308]: mismatched types\n --> src/main.rs:5:9\n  |\n5 |     let x: u32 = \"s\";\n  |            ^^^ expected `u32`, found `&str`\n\nerror: could not compile `gax` (bin \"gax\") due to 1 previous error\n"
	if _, ok := Compress("cargo test", compileFailure, 101); ok {
		t.Fatal("a compile failure was compacted")
	}
	if _, ok := Compress("cargo build", compileFailure, 101); ok {
		t.Fatal("cargo build was compacted")
	}
}

func TestFilterGoTestLeavesBuildErrorsAlone(t *testing.T) {
	buildFailure := "# example.com/x\n" +
		"./main.go:5:9: undefined: nope\n" +
		"./main.go:6:2: too many arguments in call to helper\n" +
		"    have (number)\n    want ()\n" +
		"FAIL\texample.com/x [build failed]\n"
	if result, ok := Compress("go build ./...", buildFailure, 1); ok {
		t.Fatalf("go build was compacted: %q", result.Text)
	}
	// `go test` output is compressed, but the compiler error is what the model needs: it must
	// survive whichever form is smaller.
	result, ok := Compress("go test ./...", buildFailure, 1)
	if ok {
		if !strings.Contains(result.Text, "undefined: nope") || !strings.Contains(result.Text, "build failed") {
			t.Fatalf("a go test build failure lost its compiler error: %q", result.Text)
		}
		return
	}
	if !strings.Contains(buildFailure, "undefined: nope") {
		t.Fatal("the raw build failure lost its compiler error")
	}
}

func TestFilterGoTestSaysWhatTheFailureBudgetDropped(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("--- FAIL: TestMany (0.00s)\n")
	for index := 0; index < 12; index++ {
		fmt.Fprintf(&builder, "    many_test.go:%d: got %d, want 0\n", 100+index, index)
	}
	builder.WriteString("FAIL\nFAIL\texample.com/many\t0.01s\n")
	result, ok := Compress("go test ./...", builder.String(), 1)
	if !ok {
		t.Fatal("a failing go test was not compacted")
	}
	if !strings.Contains(result.Text, "many_test.go:105: got 5, want 0") {
		t.Fatalf("go test lost a location line: %q", result.Text)
	}
	if !strings.Contains(result.Text, "… (6 more lines)") {
		t.Fatalf("go test did not say how many detail lines it dropped: %q", result.Text)
	}
}

func TestFilterPytestKeepsTracebackFrames(t *testing.T) {
	output := strings.Join([]string{
		"=================================== FAILURES ===================================",
		"_____________________________ test_deep _______________________________________",
		"",
		"    def test_deep():",
		"        helper()",
		`>       raise ValueError("boom")`,
		"E       ValueError: boom",
		"",
		"/tmp/tests/helper.py:12: ValueError",
		"=========================== short test summary info ============================",
		"FAILED tests/test_deep.py::test_deep - ValueError: boom",
		"============================== 1 failed in 0.05s ===============================",
	}, "\n")
	result, ok := Compress("pytest", output, 1)
	if !ok {
		t.Fatal("a failing pytest run was not compacted")
	}
	if !strings.Contains(result.Text, "helper.py:12: ValueError") {
		t.Fatalf("pytest lost the file:line of the failure: %q", result.Text)
	}
	if !strings.Contains(result.Text, "E       ValueError: boom") {
		t.Fatalf("pytest lost the exception line: %q", result.Text)
	}
}

func TestFilterSearchSaysWhatItTruncated(t *testing.T) {
	long := strings.Repeat("x", 200)
	output := "internal/app/app.go:10:func run() {\ninternal/app/app.go:11:" + long + "\n"
	result, ok := Compress("grep -rn run .", output, 0)
	if !ok {
		t.Fatal("grep output was not compressed")
	}
	if !strings.Contains(result.Text, "[1 long lines truncated]") {
		t.Fatalf("grep did not say how many lines it truncated: %q", result.Text)
	}
}

func TestFilterCargoTestKeepsFailures(t *testing.T) {
	result, ok := Compress("cargo test", cargoTestFailFixture, 101)
	if !ok {
		t.Fatal("a failing cargo test was not compacted")
	}
	if !strings.HasPrefix(result.Text, "FAILURES (2):") {
		t.Fatalf("cargo test did not lead with failures: %q", result.Text)
	}
	if !strings.Contains(result.Text, "test_edge_case") || !strings.Contains(result.Text, "test_overflow") {
		t.Fatalf("cargo test dropped a failure: %q", result.Text)
	}
	if !strings.Contains(result.Text, "test result: FAILED. 13 passed; 2 failed") {
		t.Fatalf("cargo test dropped its summary: %q", result.Text)
	}
	if strings.Contains(result.Text, "test utils::test_parse ... ok") {
		t.Fatalf("cargo test kept a passing test line: %q", result.Text)
	}
}

const goTestTextFailFixture = `--- FAIL: TestBad (0.00s)
    x_test.go:10: got 1, want 2
--- FAIL: TestAlsoBad (0.00s)
    x_test.go:15: boom
FAIL
FAIL	example.com/failtest	0.002s
ok  	example.com/failtest/sub	0.006s
FAIL
`

func TestFilterGoTestReadsTextOutput(t *testing.T) {
	result, ok := Compress("go test ./...", goTestTextFailFixture, 1)
	if !ok {
		t.Fatal("go test was not compacted")
	}
	if !strings.HasPrefix(result.Text, "Go test: 1 passed, 2 failed in 2 packages\n") {
		t.Fatalf("go test summary = %q", result.Text)
	}
	if !strings.Contains(result.Text, "failtest (0 passed, 2 failed)") {
		t.Fatalf("go test did not group failures by package: %q", result.Text)
	}
	if !strings.Contains(result.Text, "  [FAIL] TestBad") || !strings.Contains(result.Text, "x_test.go:10: got 1, want 2") {
		t.Fatalf("go test dropped a failure detail: %q", result.Text)
	}
}

const goTestJSONFixture = `{"Time":"2026-10-12T09:57:32.385642122+07:00","Action":"start","Package":"example.com/failtest"}
{"Time":"2026-10-12T09:57:32.388920422+07:00","Action":"run","Package":"example.com/failtest","Test":"TestGood"}
{"Time":"2026-10-12T09:57:32.389012922+07:00","Action":"pass","Package":"example.com/failtest","Test":"TestGood","Elapsed":0}
{"Time":"2026-10-12T09:57:32.389016522+07:00","Action":"run","Package":"example.com/failtest","Test":"TestBad"}
{"Time":"2026-10-12T09:57:32.389018422+07:00","Action":"output","Package":"example.com/failtest","Test":"TestBad","Output":"=== RUN   TestBad\n"}
{"Time":"2026-10-12T09:57:32.389020222+07:00","Action":"output","Package":"example.com/failtest","Test":"TestBad","Output":"    x_test.go:10: got 1, want 2\n"}
{"Time":"2026-10-12T09:57:32.389023222+07:00","Action":"fail","Package":"example.com/failtest","Test":"TestBad","Elapsed":0}
{"Time":"2026-10-12T09:57:32.389027122+07:00","Action":"fail","Package":"example.com/failtest","Elapsed":0.002}
{"Time":"2026-10-12T09:57:32.400000000+07:00","Action":"pass","Package":"example.com/failtest/sub","Test":"TestOK","Elapsed":0}
{"Time":"2026-10-12T09:57:32.401000000+07:00","Action":"pass","Package":"example.com/failtest/sub","Elapsed":0.006}
`

func TestFilterGoTestReadsJSONOutput(t *testing.T) {
	result, ok := Compress("go test -json ./...", goTestJSONFixture, 1)
	if !ok {
		t.Fatal("go test -json was not compacted")
	}
	if !strings.HasPrefix(result.Text, "Go test: 2 passed, 1 failed in 2 packages\n") {
		t.Fatalf("go test -json summary = %q", result.Text)
	}
	if !strings.Contains(result.Text, "  [FAIL] TestBad") || !strings.Contains(result.Text, "x_test.go:10: got 1, want 2") {
		t.Fatalf("go test -json dropped a failure: %q", result.Text)
	}
}

func TestFilterGoTestReportsCleanRuns(t *testing.T) {
	output := "ok  \texample.com/a\t0.01s\nok  \texample.com/b\t0.02s\n"
	result, ok := Compress("go test ./...", output, 0)
	if !ok {
		t.Fatal("a clean go test was not compacted")
	}
	if want := "Go test: 2 passed in 2 packages"; result.Text != want {
		t.Fatalf("clean go test = %q, want %q", result.Text, want)
	}
}

const pytestFailFixture = `============================= test session starts ==============================
collected 6 items

tests/test_math.py ..F..                                                [ 83%]
tests/test_more.py F                                                    [100%]

=================================== FAILURES ===================================
_________________________________ test_add ____________________________________

    def test_add():
>       assert 1 + 1 == 3
E       assert 2 == 3

tests/test_math.py:12: AssertionError
______________________________ test_subtract __________________________________

    def test_subtract():
>       raise ValueError("boom")
E       ValueError: boom

tests/test_more.py:4: ValueError
=========================== short test summary info ============================
FAILED tests/test_math.py::test_add - assert 2 == 3
FAILED tests/test_more.py::test_subtract - ValueError: boom
========================= 4 passed, 2 failed in 0.31s =========================
`

func TestFilterPytestKeepsFailures(t *testing.T) {
	result, ok := Compress("pytest", pytestFailFixture, 1)
	if !ok {
		t.Fatal("pytest was not compacted")
	}
	if !strings.HasPrefix(result.Text, "Pytest: 4 passed, 2 failed in 0.31s") {
		t.Fatalf("pytest summary = %q", result.Text)
	}
	if !strings.Contains(result.Text, "Failures:") {
		t.Fatalf("pytest dropped the failure section: %q", result.Text)
	}
	if !strings.Contains(result.Text, "[FAIL] test_add") || !strings.Contains(result.Text, "[FAIL] test_subtract") {
		t.Fatalf("pytest dropped a test name: %q", result.Text)
	}
	if !strings.Contains(result.Text, "assert 2 == 3") {
		t.Fatalf("pytest dropped an assertion: %q", result.Text)
	}
	if strings.Contains(result.Text, "collected 6 items") {
		t.Fatalf("pytest kept collection noise: %q", result.Text)
	}
}

func TestFilterPytestReadsQuietRuns(t *testing.T) {
	output := "....F.                                                                   [100%]\n1 failed, 5 passed in 0.42s\n"
	result, ok := Compress("pytest -q", output, 1)
	if !ok {
		t.Fatal("a quiet pytest run was not compacted")
	}
	if !strings.HasPrefix(result.Text, "Pytest: 5 passed, 1 failed in 0.42s") {
		t.Fatalf("quiet pytest = %q", result.Text)
	}
}

func TestFilterPytestReportsCleanRuns(t *testing.T) {
	output := "============================= test session starts ==============================\ncollected 4 items\n\ntests/test_math.py ....                                                  [100%]\n\n============================== 4 passed in 0.12s ===============================\n"
	result, ok := Compress("python -m pytest", output, 0)
	if !ok {
		t.Fatal("a clean pytest run was not compacted")
	}
	if want := "Pytest: 4 passed in 0.12s"; result.Text != want {
		t.Fatalf("clean pytest = %q, want %q", result.Text, want)
	}
}

const nodeTestFixture = `> project@1.0.0 test
> vitest run

 ✓ tests/a.test.ts (2 tests) 12ms
 ✓ tests/b.test.ts (1 test) 8ms
 ❯ tests/c.test.ts (2 tests | 1 failed) 15ms
   × renders the widget 10ms
     → expected 1 to be 2
 FAIL  tests/c.test.ts > renders the widget
AssertionError: expected 1 to be 2
 ❯ tests/c.test.ts:14:5
     Expected: 2
     Received: 1

 Test Files  1 failed | 2 passed (3)
      Tests  1 failed | 4 passed (5)
   Duration  1.20s
`

func TestFilterNodeTestCollapsesPassingTests(t *testing.T) {
	result, ok := Compress("npm test", nodeTestFixture, 1)
	if !ok {
		t.Fatal("npm test was not compacted")
	}
	if strings.Contains(result.Text, "> project@1.0.0 test") || strings.Contains(result.Text, "> vitest run") {
		t.Fatalf("npm boilerplate survived: %q", result.Text)
	}
	if !strings.Contains(result.Text, "FAIL  tests/c.test.ts > renders the widget") {
		t.Fatalf("the failing test was dropped: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Tests  1 failed | 4 passed (5)") {
		t.Fatalf("the summary was dropped: %q", result.Text)
	}
	if !strings.Contains(result.Text, "passing test lines collapsed") {
		t.Fatalf("collapsed passing tests were not reported: %q", result.Text)
	}
}

func TestFilterNodeTestLeavesPlainRunsAlone(t *testing.T) {
	plain := "added 12 packages in 3s\n"
	for _, command := range []string{"npm install", "npm run build", "npx tsc --noEmit"} {
		if _, ok := Compress(command, plain, 0); ok {
			t.Errorf("%q was compacted with nothing to strip", command)
		}
	}
}

func TestFilterNodeTestStripsBoilerplateFromOtherRuns(t *testing.T) {
	output := "> project@1.0.0 build\n> next build\n\nnpm WARN deprecated left-pad@1.0.0\n\nready - started server on 0.0.0.0:3000\n"
	result, ok := Compress("npm run build", output, 0)
	if !ok {
		t.Fatal("npm run output was not compacted")
	}
	if want := "ready - started server on 0.0.0.0:3000"; result.Text != want {
		t.Fatalf("npm run build = %q, want %q", result.Text, want)
	}
	if result.Omitted {
		t.Fatal("dropping boilerplate reported omissions")
	}
}

// ruffFixture is a concise-format run large enough that grouping beats the flat list.
func ruffFixture() string {
	var builder strings.Builder
	rules := []string{"F401", "E501", "E741", "F841", "E711"}
	for file := 0; file < 5; file++ {
		for line := 0; line < 6; line++ {
			fmt.Fprintf(&builder, "src/pkg%d/module.go:%d:1: %s Line %d is long enough to need grouping\n",
				file, line+1, rules[(file+line)%len(rules)], line+1)
		}
	}
	builder.WriteString("Found 30 errors.\n[*] 12 fixable with the `--fix` option.\n")
	return builder.String()
}

func TestFilterRuffGroupsByRuleAndFile(t *testing.T) {
	result, ok := Compress("ruff check .", ruffFixture(), 1)
	if !ok {
		t.Fatal("ruff was not compacted")
	}
	if !strings.HasPrefix(result.Text, "Ruff: 30 issues in 5 files (12 fixable)") {
		t.Fatalf("ruff summary = %q", result.Text)
	}
	if !strings.Contains(result.Text, "Top rules:\n  E501 (6x)\n  E711 (6x)\n  E741 (6x)\n  F401 (6x)\n  F841 (6x)") {
		t.Fatalf("ruff rule grouping = %q", result.Text)
	}
	if !strings.Contains(result.Text, "  src/pkg0/module.go (6 issues)") {
		t.Fatalf("ruff file grouping = %q", result.Text)
	}
	if !strings.Contains(result.Text, "  src/pkg0/module.go:1:1 F401") {
		t.Fatalf("ruff dropped a violation: %q", result.Text)
	}
	if !strings.Contains(result.Text, "… +20 more") {
		t.Fatalf("ruff did not report the capped violations: %q", result.Text)
	}
}

func TestFilterRuffRefusesUnreadableOutput(t *testing.T) {
	if _, ok := Compress("ruff check .", "error: Failed to parse src/app.py:1:1: Expected statement\n", 1); ok {
		t.Fatal("ruff compacted a message it cannot read")
	}
	if _, ok := Compress("ruff format .", ruffFixture(), 1); ok {
		t.Fatal("ruff format was compacted as a check")
	}
}

// sqlfluffFixture is a lint run large enough that grouping beats the flat list.
func sqlfluffFixture() string {
	var builder strings.Builder
	rules := []string{"LT01", "LT02", "LT12", "L010", "L044"}
	for file := 0; file < 5; file++ {
		fmt.Fprintf(&builder, "== [models/table%d.sql] FAIL\n", file)
		for line := 0; line < 6; line++ {
			fmt.Fprintf(&builder, "L:   %d | P:   1 | %s | Expected something on this line that is described\n",
				line+1, rules[(file+line)%len(rules)])
		}
	}
	return builder.String()
}

func TestFilterSqlfluffGroupsByRuleAndFile(t *testing.T) {
	result, ok := Compress("sqlfluff lint models/", sqlfluffFixture(), 1)
	if !ok {
		t.Fatal("sqlfluff was not compacted")
	}
	if !strings.HasPrefix(result.Text, "SQLFluff: 30 violations in 5 files") {
		t.Fatalf("sqlfluff summary = %q", result.Text)
	}
	if !strings.Contains(result.Text, "Top rules:\n  L010 (6x, first at models/table0.sql:4)\n  L044 (6x, first at models/table0.sql:5)") {
		t.Fatalf("sqlfluff rule grouping = %q", result.Text)
	}
	if !strings.Contains(result.Text, "  models/table0.sql (6 violations)") {
		t.Fatalf("sqlfluff file grouping = %q", result.Text)
	}
	if !strings.Contains(result.Text, "models/table0.sql:1:1 LT01 Expected something") {
		t.Fatalf("sqlfluff dropped a violation: %q", result.Text)
	}
}

func TestFilterSqlfluffLeavesPassingRunsAlone(t *testing.T) {
	output := "==== finding violations ====\nAll Finished 📜 🎉!\n"
	if _, ok := Compress("sqlfluff lint models/", output, 0); ok {
		t.Fatal("a clean sqlfluff run was compacted")
	}
}

const dockerPsFixture = `CONTAINER ID   IMAGE                  COMMAND                  CREATED        STATUS        PORTS                                                                       NAMES
557af31a13fb   openspeedtest/latest   "/docker-entrypoint.…"   15 hours ago   Up 15 hours   0.0.0.0:3000-3001->3000-3001/tcp, [::]:3000-3001->3000-3001/tcp, 8080/tcp   exciting_elgamal
048c630d47c9   postgres:16-alpine     "docker-entrypoint.s…"   19 hours ago   Up 19 hours   127.0.0.1:55432->5432/tcp                                                   vdt-task3-postgres
`

func TestFilterDockerPsKeepsEssentialFields(t *testing.T) {
	result, ok := Compress("docker ps", dockerPsFixture, 0)
	if !ok {
		t.Fatal("docker ps was not compacted")
	}
	want := strings.Join([]string{
		"[docker] 2 containers:",
		"  557af31a13fb exciting_elgamal (latest) Up 15 hours [0.0.0.0:3000-3001->3000-3001/tcp, [::]:3000-3001->3000-3001/tcp, 8080/tcp]",
		"  048c630d47c9 vdt-task3-postgres (postgres:16-alpine) Up 19 hours [127.0.0.1:55432->5432/tcp]",
	}, "\n")
	if result.Text != want {
		t.Fatalf("docker ps = %q, want %q", result.Text, want)
	}
}

func TestFilterDockerPsLeavesOtherOutputAlone(t *testing.T) {
	if _, ok := Compress("docker images", dockerPsFixture, 0); ok {
		t.Fatal("docker images was compacted as a container list")
	}
	if _, ok := Compress("docker ps", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n", 1); ok {
		t.Fatal("a docker failure was compacted")
	}
}
