describe beresp_to_resp_copying {

  // @scope: fetch
  sub test_fetch_sets_beresp_values {
    # Call vcl_fetch which sets beresp.* values
    testing.call_subroutine("vcl_fetch");
    
    # Verify that beresp.* values were set correctly
    assert.equal(beresp.status, 404);
    assert.equal(beresp.response, "Custom Not Found");
    assert.equal(beresp.http.X-Custom-Header, "custom-value");
    assert.equal(beresp.http.Cache-Control, "no-cache");
    assert.equal(beresp.http.X-Backend-ID, "server-123");
  }

  // @scope: deliver
  sub test_deliver_gets_resp_values {
    # Call vcl_deliver - the setup should copy beresp.* to resp.*
    testing.call_subroutine("vcl_deliver");
    
    # Verify that resp.* values were copied from the beresp.* values set in the previous test
    assert.equal(req.http.Resp-Status, "404");
    assert.equal(req.http.Resp-Response, "Custom Not Found");
    
    # Verify that resp.http.* headers were copied from beresp.http.* headers
    assert.equal(req.http.Resp-X-Custom-Header, "custom-value");
    assert.equal(req.http.Resp-Cache-Control, "no-cache");
    assert.equal(req.http.Resp-X-Backend-ID, "server-123");
  }
}

describe obj_to_resp_copying {

  // @scope: hit
  sub test_hit_sets_obj_values {
    # Call vcl_hit which sets obj.* values
    testing.call_subroutine("vcl_hit");
    
    # Verify that obj.* values were set correctly
    assert.equal(obj.status, 200);
    assert.equal(obj.response, "OK from Cache");
    assert.equal(obj.http.X-Cache-Header, "hit-value");
    assert.equal(obj.http.Cache-Control, "max-age=3600");
    assert.equal(obj.http.X-Cache-ID, "cache-456");
    
    # Debug: Verify vcl_hit actually ran and set obj.response
    assert.equal(req.http.Debug-Obj-Response, "OK from Cache");
  }

  // @scope: deliver
  sub test_deliver_gets_resp_values_from_obj {
    # Call vcl_deliver - the setup should copy obj.* to resp.*
    testing.call_subroutine("vcl_deliver");
    
    # Verify that resp.* values were copied from the obj.* values set in the previous test
    assert.equal(req.http.Resp-Status, "200");
    assert.equal(req.http.Resp-Response, "OK from Cache");
    
    # Verify that resp.http.* headers were copied from obj.http.* headers
    assert.equal(req.http.Resp-X-Cache-Header, "hit-value");
    assert.equal(req.http.Resp-Cache-Control, "max-age=3600");
    assert.equal(req.http.Resp-X-Cache-ID, "cache-456");
  }
}