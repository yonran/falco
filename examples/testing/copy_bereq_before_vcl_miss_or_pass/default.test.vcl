// Test that bereq is properly set up when calling vcl_miss from tests
sub test_bereq_set_before_vcl_miss {
  set req.url = "/req_before_vcl_miss";
  set req.http.host = "miss.example.com";
  
  // tester should copy req to bereq right before running vcl_miss
  testing.call_subroutine("vcl_miss");
  
  // Verify that bereq was set up correctly
  assert.equal(req.http.Backend-URL, "/req_before_vcl_miss");
  assert.equal(req.http.Backend-Host, "miss.example.com");
}

sub test_bereq_set_before_vcl_pass {
  set req.url = "/req_before_vcl_pass";
  set req.http.host = "pass.example.com";
  
  // tester should copy req to bereq right before running vcl_miss
  testing.call_subroutine("vcl_pass");
  
  // Verify that bereq was set up correctly
  assert.equal(req.http.Backend-URL, "/req_before_vcl_pass");
  assert.equal(req.http.Backend-Host, "pass.example.com");
}
