"""对着已启动的 api 复跑 P0 登录与 API Key。未设置 CAKERDESK_E2E_URL 时跳过。"""

import json
import os
import urllib.request

import pytest

BASE = os.environ.get("CAKERDESK_E2E_URL", "")

pytestmark = pytest.mark.skipif(not BASE, reason="set CAKERDESK_E2E_URL")


def post(path, payload, cookie=""):
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json", **({"Cookie": "cd_session=" + cookie} if cookie else {})},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req) as res:
            return res.status, res.read(), res.headers.get("Set-Cookie", "")
    except urllib.error.HTTPError as err:
        return err.code, err.read(), ""


def test_e_auth_login():
    status, body, cookie = post(
        "/v1/auth/login",
        {"email": os.environ["CAKERDESK_E2E_EMAIL"], "password": os.environ["CAKERDESK_E2E_PASSWORD"], "tenant": os.environ["CAKERDESK_E2E_TENANT"]},
    )
    assert status == 200
    assert "cd_session=" in cookie
    assert b"@" in body
