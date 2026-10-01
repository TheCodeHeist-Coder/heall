file:///tmp/claude-1000/-home-codeheist-Desktop-heall/4e736679-6afc-48aa-8708-b06b70d7b79c/scratchpad/tapsample/test/broken.test.js:1
import { nope } from "../src/calc.js";
         ^^^^
SyntaxError: The requested module '../src/calc.js' does not provide an export named 'nope'
    at #asyncInstantiate (node:internal/modules/esm/module_job:326:21)
    at async ModuleJob.run (node:internal/modules/esm/module_job:429:5)
    at async node:internal/modules/esm/loader:639:26
    at async asyncRunEntryPointWithESMLoader (node:internal/modules/run_main:101:5)

Node.js v24.15.0
✖ test/broken.test.js (57.919534ms)
▶ div
  ✔ divides (1.282459ms)
  ✖ handles zero: 'quoted' (parens) (0.393948ms)
✖ div (3.481718ms)
﹣ skipped one (0.167788ms) # SKIP
⚠ todo one (0.780732ms) # TODO
ℹ tests 5
ℹ suites 1
ℹ pass 1
ℹ fail 2
ℹ cancelled 0
ℹ skipped 1
ℹ todo 1
ℹ duration_ms 123.844346

✖ failing tests:

test at test/broken.test.js:1:1
✖ test/broken.test.js (57.919534ms)
  'test failed'

test at test/calc.test.js:6:3
✖ handles zero: 'quoted' (parens) (0.393948ms)
  RangeError: division by zero
      at div (file:///tmp/claude-1000/-home-codeheist-Desktop-heall/4e736679-6afc-48aa-8708-b06b70d7b79c/scratchpad/tapsample/src/calc.js:2:22)
      at TestContext.<anonymous> (file:///tmp/claude-1000/-home-codeheist-Desktop-heall/4e736679-6afc-48aa-8708-b06b70d7b79c/scratchpad/tapsample/test/calc.test.js:6:62)
      at Test.runInAsyncScope (node:async_hooks:227:14)
      at Test.run (node:internal/test_runner/test:1201:25)
      at Suite.processPendingSubtests (node:internal/test_runner/test:831:18)
      at Test.postRun (node:internal/test_runner/test:1330:19)
      at Test.run (node:internal/test_runner/test:1258:12)
      at async Promise.all (index 0)
      at async Suite.run (node:internal/test_runner/test:1619:7)
      at async startSubtestAfterBootstrap (node:internal/test_runner/harness:385:3)

test at test/calc.test.js:9:1
⚠ todo one (0.780732ms) # TODO
  AssertionError [ERR_ASSERTION]: x
      at TestContext.<anonymous> (file:///tmp/claude-1000/-home-codeheist-Desktop-heall/4e736679-6afc-48aa-8708-b06b70d7b79c/scratchpad/tapsample/test/calc.test.js:9:49)
      at Test.runInAsyncScope (node:async_hooks:227:14)
      at Test.run (node:internal/test_runner/test:1201:25)
      at Test.processPendingSubtests (node:internal/test_runner/test:831:18)
      at Test.postRun (node:internal/test_runner/test:1330:19)
      at Test.run (node:internal/test_runner/test:1258:12)
      at async Test.processPendingSubtests (node:internal/test_runner/test:831:7) {
    generatedMessage: false,
    code: 'ERR_ASSERTION',
    actual: undefined,
    expected: undefined,
    operator: 'fail',
    diff: 'simple'
  }
