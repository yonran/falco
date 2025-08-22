backend test_backend {
  .host = "example.com";
  .port = "80";
}

sub vcl_recv {
  set req.backend = test_backend;
}

sub vcl_fetch {
  # Set arbitrary beresp.* values to test that they get copied to resp.*
  set beresp.status = 404;
  set beresp.response = "Custom Not Found";
  set beresp.http.X-Custom-Header = "custom-value";
  set beresp.http.Cache-Control = "no-cache";
  set beresp.http.X-Backend-ID = "server-123";
  
  return(deliver);
}

sub vcl_hit {
  # Set arbitrary obj.* values to test that they get copied to resp.*
  set obj.status = 200;
  set obj.response = "OK from Cache";
  set obj.http.X-Cache-Header = "hit-value";
  set obj.http.Cache-Control = "max-age=3600";
  set obj.http.X-Cache-ID = "cache-456";
  
  # Debug: Verify obj.response was set
  set req.http.Debug-Obj-Response = obj.response;
  
  return(deliver);
}

sub vcl_deliver {
  # Transfer resp values to req.http for test verification
  # This tests that resp.* was copied from beresp.* or obj.* before vcl_deliver was called
  set req.http.Resp-Status = resp.status;
  set req.http.Resp-Response = resp.response;
  
  # Transfer resp.http headers to req.http for test verification
  set req.http.Resp-X-Custom-Header = resp.http.X-Custom-Header;
  set req.http.Resp-Cache-Control = resp.http.Cache-Control;
  set req.http.Resp-X-Backend-ID = resp.http.X-Backend-ID;
  set req.http.Resp-X-Cache-Header = resp.http.X-Cache-Header;
  set req.http.Resp-X-Cache-ID = resp.http.X-Cache-ID;
  
  return(deliver);
}