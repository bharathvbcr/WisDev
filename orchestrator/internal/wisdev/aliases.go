package wisdev

import "net/http"

func rewriteWisdevRequestPath(mux *http.ServeMux, targetPath string, w http.ResponseWriter, r *http.Request) {
	rewritten := r.Clone(r.Context())
	rewritten.URL.Path = targetPath
	rewritten.URL.RawPath = targetPath
	mux.ServeHTTP(w, rewritten)
}
