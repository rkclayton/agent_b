# Third-party notices

Agent_b includes these direct and transitive Go modules:

- `github.com/refraction-networking/utls` v1.8.2 (BSD-3-Clause; Copyright (c) 2009 The Go Authors)
- `github.com/ledongthuc/pdf` v0.0.0-20260907135840-6c8c28e0e8a0 (BSD-3-Clause)
- `github.com/andybalholm/brotli` v1.0.6 (MIT; Copyright (c) 2009, 2010, 2013-2016 by the Brotli Authors)
- `github.com/dustin/go-humanize` v1.0.1 (MIT)
- `github.com/google/uuid` v1.6.0 (BSD-3-Clause)
- `github.com/klauspost/compress` v1.17.4 (BSD-3-Clause; Copyright (c) 2012 The Go Authors; Copyright (c) 2019 Klaus Post; included `internal/snapref` code Copyright (c) 2011 The Snappy-Go Authors)
- `github.com/mattn/go-isatty` v0.0.20 (MIT)
- `github.com/ncruces/go-strftime` v0.1.9 (MIT)
- `github.com/remyoudompheng/bigfft` v0.0.0-20230129092748-24d4a6f8daec (BSD-3-Clause)
- `golang.org/x/crypto` v0.48.0, `golang.org/x/exp` v0.0.0-20250620022241-b7579e27df2b, `golang.org/x/net` v0.50.0, and `golang.org/x/sys` v0.41.0 (BSD-3-Clause; Copyright 2009 The Go Authors)
- `modernc.org/libc` v1.66.3, `modernc.org/mathutil` v1.7.1, `modernc.org/memory` v1.11.0, and `modernc.org/sqlite` v1.38.2 (BSD-3-Clause)

## BSD-3-Clause terms

Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the following disclaimer in the documentation and/or other materials provided with the distribution.
3. Neither the name of Google Inc. nor the names of its contributors may be used to endorse or promote products derived from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

## MIT terms

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

## Microsoft Edge WebView2 loader (`WebView2Loader.dll`)

Agent_b ships one native binary it did not build: `WebView2Loader.dll`, from the
`Microsoft.Web.WebView2` SDK package (version 1.0.4191.47), beside `Agent_b.exe`. It locates the
separately-installed Microsoft Edge WebView2 Runtime so Agent_b can host its own window; the browser
engine itself is that runtime and is not redistributed here.

The file is used unmodified and as-supplied, Authenticode-signed by Microsoft Corporation. Its
SHA-256 and signature are pinned in `scripts/webview2-loader.json` and verified at build and at
install. Redistribution follows the Microsoft Software License Terms for the Microsoft Edge WebView2
SDK, which permit distributing the loader with an application that uses it.

Agent_b does not embed this DLL in its executable and never maps it into memory itself; it is loaded
by Windows from the application directory. See `SECURITY.md` for why.
