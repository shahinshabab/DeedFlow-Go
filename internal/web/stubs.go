package web

// Temporary placeholders for pages whose modules are ported later; each one
// is removed as its module lands.
func (a *App) registerStubs() {
	stub := func(c *Ctx) { c.NotFound() }
	a.Route("filereader:index", "file-reader/", a.LoginRequired(stub))
	a.Route("filereader:filing", "file-reader/filing-sheets/", a.LoginRequired(stub))
}
