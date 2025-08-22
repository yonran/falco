sub vcl_recv {
  declare local var.status INTEGER;
  declare local var.response STRING;
  set var.status = std.strtol(querystring.get(req.url, "status"), 10);
  set var.response = querystring.get(req.url, "response");

  error var.status var.response;
}

sub vcl_error {
  # Transfer obj.status and obj.response to req.http headers for test verification
  # This tests that obj.* variables are accessible in vcl_error scope
  set req.http.Error-Status = obj.status;
  set req.http.Error-Response = obj.response;
  return(deliver);
}