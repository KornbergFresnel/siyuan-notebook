import * as assert from "node:assert/strict";
import test from "node:test";
import {resolveProviderTestResult} from "./aiProviderTestResult";

test("provider test result reports success when the model is usable", () => {
    assert.deepEqual(resolveProviderTestResult({matched: true, available: ["glm-5.3"]}), {
        success: true,
        modelNotFound: false,
        message: "",
    });
});

test("provider test result prefers the server error over the model-not-found guess", () => {
    assert.deepEqual(resolveProviderTestResult({
        matched: false,
        available: ["glm-5.3"],
        msg: "error, status code: 400, status: 400 Bad Request, message: API 调用参数有误",
    }), {
        success: false,
        modelNotFound: false,
        message: "error, status code: 400, status: 400 Bad Request, message: API 调用参数有误",
    });
});

test("provider test result detects a model missing from the available list", () => {
    assert.deepEqual(resolveProviderTestResult({matched: false, available: ["glm-4.6"]}), {
        success: false,
        modelNotFound: true,
        message: "",
    });
});

test("provider test result falls back to a plain failure without a list or a message", () => {
    assert.deepEqual(resolveProviderTestResult({matched: false, available: []}), {
        success: false,
        modelNotFound: false,
        message: "",
    });
});
