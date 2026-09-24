# Third-party data in this package

`common-passwords.txt.gz` is derived from
`Passwords/Common-Credentials/xato-net-10-million-passwords-100000.txt` of
[SecLists](https://github.com/danielmiessler/SecLists) (upstream file
SHA-256 `1472aafa2561df5e3293aee252aee3ca660c12b399a283cf808bb01b39be388b`),
reduced to entries of at least 8 characters, lower-cased, sorted and
de-duplicated (38,451 entries):

```sh
tr -d '\r' < xato-net-10-million-passwords-100000.txt | awk 'length($0)>=8' |
  tr 'A-Z' 'a-z' | LC_ALL=C sort -u | gzip -9 -n > common-passwords.txt.gz
```

SecLists is distributed under the MIT License:

> Copyright (c) 2018 Daniel Miessler
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.
