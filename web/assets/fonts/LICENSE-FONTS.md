# Fonts shipped with Agent_b

Agent_b runs offline, so every typeface it offers from its own list is shipped
with it: a font named but absent falls back silently to something else, and the
reader would never know they were not getting what they chose. Windows' own
faces are offered as well; those are not redistributed here.

Each family below is licensed under the **SIL Open Font License, Version 1.1**.
The full licence text is in [`LICENSE-OFL.txt`](LICENSE-OFL.txt) beside this file
and applies to all of them.

| Family | Weight shipped | File | Bytes | Copyright |
|---|---|---|---|---|
| IBM Plex Sans | 400, 500 | `ibm-plex-sans-latin-{400,500}-normal.woff2` | 22,588 + 24,184 | Copyright © 2017 IBM Corp., with Reserved Font Name "Plex" |
| IBM Plex Mono | 400, 500 | `ibm-plex-mono-latin-{400,500}-normal.woff2` | 14,708 + 14,888 | Copyright © 2017 IBM Corp., with Reserved Font Name "Plex" |
| Atkinson Hyperlegible | 400 | `atkinson-hyperlegible-latin-400-normal.woff2` | 17,208 | Copyright 2020 Braille Institute of America, Inc. |
| OpenDyslexic | 400 | `opendyslexic-latin-400-normal.woff2` | 115,280 | Copyright (c) 2019-07-29, Abbie Gonzalez (https://abbiecod.es \| support@abbiecod.es), with Reserved Font Name OpenDyslexic. Copyright (c) 12/2012 - 2019 |

All four families were taken from the `@fontsource` packages at version 5.3.0,
the same source as the IBM Plex files Agent_b has always shipped, subset to
Latin and served as WOFF2 from `web/css/tokens.css`.
