backend test_backend {
  .host = "example.com";
  .port = "80";
}

sub vcl_recv {
  set req.backend = test_backend;
}

sub vcl_fetch {
  # Transfer beresp.cacheable to req.http for test verification
  # This tests that beresp.cacheable was set up before vcl_fetch was called
  if (beresp.cacheable) {
    set req.http.Beresp-Cacheable = "true";
  } else {
    set req.http.Beresp-Cacheable = "false";
  }
  
  # Also transfer beresp.ttl for verification
  set req.http.Beresp-TTL = beresp.ttl;
  
  return(deliver);
}