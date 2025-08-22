// @scope: fetch
// Test that beresp.cacheable is set correctly before vcl_fetch
sub test_beresp_cacheable_set_before_vcl_fetch {
  # Call vcl_fetch - the setup should set beresp.cacheable based on status code
  testing.call_subroutine("vcl_fetch");
  
  # Verify that beresp.cacheable was set to true for 200 OK status
  # (200 is cacheable by default in the cache.IsCacheableStatusCode function)
  assert.equal(req.http.Beresp-Cacheable, "true");
  
  # Verify that beresp.ttl was also set (should be non-zero for cacheable responses)
  # The determineCacheTTL function sets it to 2 minutes by default (120.000 seconds)
  assert.equal(req.http.Beresp-TTL, "120.000");
}