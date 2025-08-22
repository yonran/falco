describe test_vcl_error_can_access_obj_status {
  sub test_recv {
    set req.url = "/?status=601&response=my%20bad";
    testing.call_subroutine("vcl_recv");
  }
  sub test_error {
    testing.call_subroutine("vcl_error");
    assert.equal(req.http.Error-Response, "my bad");
    assert.equal(req.http.Error-Status, "601");
  }
}
