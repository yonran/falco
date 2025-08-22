sub vcl_miss {
  set req.http.Backend-URL = bereq.url;
  set req.http.Backend-Host = bereq.http.host;
  return(fetch);
}

sub vcl_pass {
  set req.http.Backend-URL = bereq.url;
  set req.http.Backend-Host = bereq.http.host;
  return(fetch);
}
