# Palm OS development with Claude Code: project context

Everything FuelLog (github.com/lasergirl-ca/palm-fuellog) learned about building a native
Palm OS 5 application with prc-tools in 2026, written as a context file for the next
project. Copy it into the new repository as `CLAUDE.md` (or `docs/` and point `CLAUDE.md`
at it), replace the FuelLog-specific names, and delete what the new app does not need.
Every statement here was verified on a Palm T|X or in the toolchain; dates say when.

## 1. Target

- Palm OS 5 (T|X, 5.4.9) as the primary device; 68K code only, run under PACE. Pure 68K also
  runs on older Palms (minimum ROM 3.0 checked at launch) and in CloudpilotEmu. Do not write
  ARM native code unless profiling says so.
- Screen: 320 x 480 pixels, addressed as 160 x 240 standard coordinates with the input
  area (DIA) collapsed, 160 x 160 with it open. Design forms for 160 x 160; let them grow.
- Owner's workflow: she tests on the real device from a `.prc` copied to `/PALM/Launcher/`
  on the SD card. Nothing counts as verified until she has seen it there.

## 2. Toolchain

**Never run `brew install prc-tools`.** GCC 2.95's `cc1` does not build on Apple Silicon
(configure fails under Clang 21; a patched build compiles but `cc1` segfaults). Use the
Docker image. The full build takes about 15 s, incremental about 4 s.

`toolchain/Dockerfile` (copy as is):

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl make gcc libc6-dev bison flex && rm -rf /var/lib/apt/lists/*
# prc-tools-remix 2.3.6: GCC 2.95.3 for m68k-palmos, the first release that builds with GCC 15
RUN curl -fsSL -o /tmp/prc-tools.deb \
      https://github.com/jichu4n/prc-tools-remix/releases/download/v2.3.6/prc-tools-remix_2.3.6.bookworm_amd64.deb \
 && apt-get update && apt-get install -y --no-install-recommends /tmp/prc-tools.deb \
 && rm -f /tmp/prc-tools.deb && rm -rf /var/lib/apt/lists/*
# Palm OS SDK 5r3 where prc-tools looks for it
RUN mkdir -p /opt/palmdev \
 && curl -fsSL https://github.com/jichu4n/palm-os-sdk/archive/5.0.3.tar.gz | tar xz -C /tmp \
 && mv /tmp/palm-os-sdk-5.0.3/sdk-5r3 /opt/palmdev/sdk-5r3 && rm -rf /tmp/palm-os-sdk-5.0.3 \
 && palmdev-prep
# PilRC 3.2
RUN curl -fsSL -o /tmp/pilrc.tar.gz https://downloads.sourceforge.net/project/pilrc/pilrc/3.2/pilrc-3.2.tar.gz \
 && tar xzf /tmp/pilrc.tar.gz -C /tmp && cd /tmp/pilrc-3.2 && ./unix/configure && make && make install \
 && rm -rf /tmp/pilrc-3.2 /tmp/pilrc.tar.gz
WORKDIR /src
CMD ["make"]
```

Make targets to provide from day one:

| Target | Does |
|---|---|
| `make docker-image` | `docker build --platform linux/amd64 -t <app>-toolchain toolchain` (once) |
| `make docker` | `docker run --rm --platform linux/amd64 -v "$(CURDIR)":/src <app>-toolchain make` |
| `make test` | host unit tests with the system `cc` (section 9) |
| `make check` | `tools/prcinfo.py App.prc --expect-code N` lists resources and checks the code-resource count |

Compiler flags: `-O2 -Wall -palmos5r3 -Irsc -Isrc -MMD`. The SDK flag is `-palmos5r3`, not
`-palmos5`. GCC 2.95 writes the `-MMD` file into the current directory with a bare target
name; the rule has to move it and rewrite the target:

```make
$(BUILD)/%.o: src/%.c | $(BUILD)
	$(CC) $(CFLAGS) -c $< -o $@
	sed -e 's|^$*\.o:|$@:|' $*.d > $(BUILD)/$*.d && rm -f $*.d
-include $(OBJS:.o=.d)
```

Link: `m68k-palmos-multigen -b build/app-sections app.def` makes the section table;
compile its `.s`; link all objects plus `sections.o` plus the `.ld`; then
`pilrc -q -I rsc rsc/app.rcp build` and `build-prc -o App.prc app.def build/App build/*.bin`.
Put a build stamp in the About box (`-DAPP_BUILD='"$(shell date +%m%d-%H%M)"'`) so a device can
be matched to a build.

A native alternative exists (github.com/User7142/palmdev-macos: pre-built m68k and ARM
prc-tools for Apple Silicon) but was not adopted; Docker was fast enough. Its PilRC has the
same BMP bug as below.

## 3. The `.def` file and code sections

```
application { "App" CRTR stack=16384 }
multiple code { sec2 forms io }
```

- **Stack:** the prc-tools default is 4 KB. Set `stack=16384`; a deep form handler
  overflows 4 KB silently.
- **Each code resource is capped at 32 KB** by the 16-bit branch range ("region coderes is
  full" at link time). Plan sections from the start; every new form goes into one. FuelLog
  ended with eleven. The word `export` is a `.def` keyword: do not name a section that.
- Put the section attribute on **prototypes**, in the header, not only on the definition.
  GCC places a function by the first declaration it has seen. A caller that cannot see the
  attribute emits a PC-relative branch into the wrong code resource, and on the device that
  is a blank form with no crash. Static functions need a tagged prototype too
  (`tools/sectionize.py MACRO file.c` writes them, idempotently).
- `src/sections.h` defines the macros as `__attribute__((section("forms")))` under
  `__palmos__` and empty otherwise, so the same code compiles on the host.
- **A `static const` table inside a sectioned function breaks.** It is emitted into the
  code section but addressed A5-relative (an `END16` relocation), so it reads garbage on
  the device: vanished buttons, a Fatal Exception. Use plain `static` (lands in `.data`) or a
  file-scope table. String literals are fine.
- Run a checker at link time. FuelLog's `tools/check-sections.sh` uses `m68k-palmos-objdump
  -t/-r` to fail the build on any `DISP16` relocation to a function in another section and
  on any `END16` relocation into a code section. Copy it; it has caught every one of these
  since.

## 4. The compiler and the language

- GCC 2.95.3: C89. Declarations before statements in every block. No `//` is tolerated but
  avoid it; no `inline`, no designated initialisers, no mixed declarations.
- **`int` is 16 bits.** `err ? err : -1` with a `UInt16 err` widens to 65535. Cast such
  expressions to `Int32`. Loop counters over record counts need `UInt16` with care at 65535.
- `unsigned long long` works (libgcc `__muldi3` and `__udivdi3` cost about 1.6 KB in the
  main section). Use it for intermediates like cents times a rate times 1e6. Never link
  floating point; integer IEEE-754 decoding by hand is fine for reading XLS files.
- `src/ptypes.h`: under `__palmos__` include `PalmTypes.h`, else typedef `UInt8/Int8/
  UInt16/Int16/UInt32/Int32` from `stdint.h`. Every non-UI module includes it, not `PalmOS.h`,
  so it compiles on the host.
- Palm's `StrNCat(dst, src, n)`: `n` is the maximum length of the **result**, not the count
  appended. `StrIToA`, `StrPrintF` have no width padding to speak of; format numbers yourself.
- Fixed point everywhere: store metric values scaled (mL, Wh, ten-thousandths of a currency
  unit per litre), convert for display. Check 20-year sums against `UInt32` on paper.

## 5. Memory

- **The A5 data segment (data + bss) is 32 KB** ("region datares is full"). Big buffers go
  on the heap (`MemPtrNew`) or the stack, never static. FuelLog's `.data` is under 1 KB; the
  bss is what fills.
- **A record is capped near 64 KB**; `DmNewRecord` fails above. Chunk large blobs into 32 KB
  pieces and rejoin.
- Heap: the T|X has plenty (tens of KB for a 32 KB inflate window plus buffers was fine);
  older Palms do not. Free on every exit path.

## 6. Resources and PilRC

- `rsc/resources.h` holds every ID as `#define`, included by both the `.rcp` and the C code.
  Resource names can clash with SDK names (`NoteField` is in `UIResources.h`): prefix with
  the form.
- `VERSION "1.0rc5"` in the `.rcp` is what the Launcher shows; keep the About text in step.
- Icons: `ICONFAMILYEX` / `SMALLICONFAMILYEX` with `BPP 1`, `BPP 8 TRANSPARENT 255 0 255`,
  and `DENSITY 144` copies at 44 x 44 and 30 x 18 (resources tAIB 1000/1001 and tAIN).
- **PilRC 3.2 cannot read a normal BMP on a 64-bit host**: it declares the header's width and
  height as C `long` (8 bytes), so every file fails with "Bitmap not monochrome, 16, 256,
  16bit, 24bit or 32bit color". FuelLog's `tools/mkicons.py` writes BMPs with a 64-byte
  padded info header (biSize 64, 8-byte width/height at offsets 8/16, planes/bitcount at
  24/26, compression at 28, clrUsed at 56) that only PilRC opens. Copy that writer.
- "Form has editable field(s) without Graffiti State Indicator" warnings are harmless.
- Popup lists: `VISIBLEITEMS` must equal the item count or the list scrolls; height is
  11 per item.
- Tables: FuelLog hand-draws its lists with `WinDrawChars` and its own row index instead of
  a `TABLE` or `LIST` resource; mixed fonts per column, footers and a vehicle filter were
  easier that way, and it fits in a section with its form.

## 7. UI patterns that work

- **Form handlers:** register in `frmLoadEvent` with a `switch` over form IDs calling
  `FrmSetEventHandler`. A form whose handler never registered shows a blank white screen and
  idles; it does not crash. (A scripted edit that silently failed caused exactly this.)
- **DIA (input area):** at form open, if `FtrGet(pinCreator, pinFtrAPIVersion)` succeeds:
  `FrmSetDIAPolicyAttr(form, frmDIAPolicyCustom)`, `PINSetInputTriggerState(
  pinInputTriggerEnabled)`, `WinSetConstraintsSize(win, 160, 225, 225, 160, 160, 160)`,
  `PINSetInputAreaState(pinInputAreaUser)`. Then fit the form: `WinGetDisplayExtent`, set the
  window bounds, and pin bottom buttons to the height the form window **really** has
  (`FrmGetFormBounds` after the resize, not the display extent). Refit on
  `winDisplayChangedEvent`. The T|X delivers 160 x 224 collapsed, not 225.
- Rows that scroll under pinned bottom buttons must be hidden (`FrmHideObject`), because
  `CtlSetLabel` redraws a control over the buttons.
- `CtlSetLabel`, `FrmSetTitle`, `FrmCopyLabel` targets: the string must stay alive. Keep
  static buffers for each. `FrmCopyLabel` copies, the others do not.
- `FrmGetControlGroupSelection` returns the **object index**; `FrmSetControlGroupSelection`
  takes the **ID**. Compare indices to indices or every push group reads as its first option.
- Hi-res drawing: `WinSetCoordinateSystem(kCoordinatesDouble)` after checking
  `sysFtrNumWinVersion >= 4` and `WinScreenGetAttribute(winScreenDensity)`; restore after.
- 5-way navigator: `vchrRockerCenter` 0x0136, `vchrNavChange` 0x0507 with the select bit,
  `vchrPageUp/Down`. Palm's convention: page keys page until centre highlights a row, then
  up/down move the highlight and centre opens.
- Modal dialogs over a form with typed fields: `FrmDoDialog` keeps the parent's field text.
- Date and time pickers: `SelectDay` and `SelectOneTime`. Read section 10 before using
  `SelectDay`.
- Stock fonts: `stdFont` for labels; `boldFont` is too wide for a 160-px row of labels.

## 8. Data

- Databases: `DmCreateDatabase(0, name, creator, type, false)`, then open
  `dmModeReadWrite`, write an AppInfo block with a schema version through `DmNewHandle` on
  the **open** reference (writing it before opening left it unset), sorted insert with
  `DmFindSortPosition` and a compare function, `DmQuickSort` after bulk changes.
- **Test the open path with a second launch.** The first run exercises create; the bugs are
  in open and version-check.
- Packed record layouts with explicit byte offsets, documented in a table in the design
  spec, with host tests that pack and unpack every field. Never write a struct to a record.
- Prefs: a versioned struct via `PrefGetAppPreferences`/`PrefSetAppPreferences`, an upgrade
  function on the host test path, and spare slots (FuelLog's rate table has eight entries
  for six currencies) so adding an enum value needs no new version.
- Growing a record layout without a schema bump: put new fields in a trailer after the
  terminating NUL of the record's text (or after the fixed part when there is no text).
  Old readers stop at the NUL and never see it; new readers take it when enough bytes
  remain. FuelLog's sync identity (uid, modified) lives there, and rc5 builds, old backups
  and the Go reader needed no change. Grow the trailer the same way: readers take each
  field only when its bytes are there, and a writer that does not know a field keeps the
  record short, so the field must have a "never set" value (FuelLog's clockShift: 0xFFFF).
- Character set: Palm Latin is Windows-1252 (0x80 to 0x9F are the cp1252 specials, 0xA0 to
  0xFF are Latin-1). Convert to and from UTF-8 at the CSV/XLSX boundary with a 32-entry
  table. The euro is 0x80 and the pound 0xA3 in both Palm Latin and PDF WinAnsi, so they pass
  through PDF unchanged. Read number separators from the Formats panel
  (`LocGetNumberSeparators(PrefGetPreference(prefNumberFormat), …)`).
- Dates: see section 10. Store your own `UInt16` days since 1904, never `DateType`.

## 9. Testing without the device

- Every non-UI module (calculations, record packing, units, prefs upgrade, dates, CSV,
  XLSX/XLS/PDF writers and readers, inflate) is plain C that compiles with the host `cc`
  through `ptypes.h`. One `tests/test_x.c` per module with a `check(name, got, want)` helper
  that prints `ok`/`FAIL` and counts failures; `make test` runs them all. FuelLog has 14
  suites plus Python checkers (`check_xlsx.py` opens the output with openpyxl, `check_inflate.py`
  compares with `zlib`) and a Go reader for backup files that doubles as a format test.
- `make check` (`tools/prcinfo.py`) lists every resource in the `.prc` and fails if the code
  resource count changed unexpectedly: it catches a section that silently merged.
- Before handing over an untested build, run a read-only review agent over the new forms
  with a checklist (index-vs-ID, persistent label strings, section attributes on prototypes,
  heap vs static buffers, 16-bit widening, hidden rows under buttons, open-path on second
  launch). Two such passes found 14 and 17 real bugs.
- On the device: an About box line with build stamp, display height, window height and
  redraw count told apart layout bugs; temporary breadcrumbs drawn at the top of the screen
  from `PilotMain` found a launch-time blank screen. Remove them afterwards.
- CloudpilotEmu (cloudpilot-emu.github.io) runs the `.prc` in a browser with a T|X or older
  ROM when the device is not at hand; set the emulated Palm's clock back in its own Date & Time panel to test the shifted-clock paths.
- Measured on the T|X (2026-10-22): SD write 86 KB/s in 4 KB chunks, 1.6 MB in 32 KB pieces
  added about 1 s; database export of 1,000 records 150 ms, import 360 ms; inflate under
  PACE 94 KB/s with a 9-bit lookup table (21 KB/s without). **Buffer card I/O at 32 KB.**

## 10. Dates and the 2031 problem

`DateType` ends in 2031 and `TimGetSeconds` in 2040. Owners set the clock back 28 years, by
hand or with DateFix (github.com/User7142/palm-datefix), which also patches the OS to show
real years and publishes its offset as `FtrGet('DtFx', 1, &years)`. The full guide is
`docs/Palm-Clocks-and-DateFix.md` in the FuelLog repository; the short form:

- Store real dates in your own type; do all calendar arithmetic in host-tested code.
- Touch the system clock in exactly three places: one function for today (adds the shift),
  one `SelectDay` wrapper (DateFix's picker speaks internal years; convert in and out, and
  move a "Today" result forward), one alarm wrapper (`TimDateTimeToSeconds` wants device
  time: subtract the shift).
- Detect: the DateFix feature first, then a device year more than 14 years behind the build
  year as a hand-set 28-year shift. No checkbox.
- Never pass a real year to `DateToAscii`, `DayOfWeek`, `DateTemplateToAscii` on a DateFix
  Palm: they add the offset again.
- Migrate previously stored device dates once per launch and after restore, within a window
  around the device's today, so the operation is idempotent.

## 11. Launch codes, alarms, beaming

- `PilotMain` is called for many codes besides `sysAppLaunchCmdNormalLaunch`, often
  **without globals and without the jump table** (`sysAppLaunchFlagSubCall` set, or no
  `sysAppLaunchFlagNewGlobals`). Handlers for `sysAppLaunchCmdExgReceiveData`,
  `sysAppLaunchCmdDisplayAlarm`, `sysAppLaunchCmdSystemReset` must live in the main code
  section, use no globals, and do the minimum: write the received data to a file or
  database and let the next normal launch process it; show the alarm alert; re-arm the alarm
  from prefs after a reset.
- `ExgReceive` returns short: loop until the requested count, zero, or an error.
- Register for beams with `ExgRegisterData` at normal launch; receiving into a staging
  database and offering Merge/Replace on the next launch worked well.
- Alarms: `AlmSetAlarm(cardNo, dbID, 0, seconds, true)`; store the pending time in prefs
  and re-set it on `sysAppLaunchCmdSystemReset`; `sysAppLaunchCmdAlarmTriggered` needs no
  work, the system plays the sound.

## 12. SD card and files

- `VFSVolumeEnumerate` for the first mounted volume; app files under
  `/PALM/Programs/<App>/` with subfolders (Export, Import, Backup); create each level
  (`VFSDirCreate`) and ignore "already exists".
- Backups: `VFSExportDatabaseToFile` and `VFSImportDatabaseFromFile` per database. Close
  the database, delete it, import, reopen, then run any migration. Keep N sets, name them
  by date, sort names descending for the restore list.
- `.pdb` files on the card are readable on the Mac; a small Go or Python reader of the
  packed layouts is a worthwhile fixture and test.
- Zip output for XLSX: STORED entries with a data descriptor avoid seeking; the Numbers,
  Excel and openpyxl readers all accept it.

## 13. Project conventions that paid off

- One markdown file per milestone (`docs/M1.md` …) with "what was built", "what to check on
  the T|X" as a numbered list, and "device result". The owner works through the list on the
  device and reports back; nothing is marked done before that.
- A design spec with the record layouts as byte tables, kept in the repo as markdown; the
  live Claude Doc drifts, the export is what the code follows.
- Interim test builds go to the owner from the scratchpad with a descriptive name
  (`App-rc4-datefix.prc`), never as extra `.prc` files in the repo root. Only `App.prc` and
  `releases/` belong in the tree.
- Releases land in three places: an annotated tag, a GitHub pre-release with the `.prc`
  attached, and a copy under `releases/` with a row in `releases/README.md`; then the owner
  updates the PalmDB listing (palmdb.net) herself.
- Generated headers that change per build (FuelLog bakes the day's exchange rates in at
  build time) are committed with the release, and the compiler checks their shape against
  the enum they must match.
- Scripted source edits (Python `str.replace`) must **assert the match**; a silent miss cost
  half a day.
- Commits are the owner's alone: no `Co-Authored-By` trailer.
- The licence is GPLv3 with the design documents included.

## 14. Links

- prc-tools-remix: github.com/jichu4n/prc-tools-remix (releases carry the Debian package)
- Palm OS SDK 5r3: github.com/jichu4n/palm-os-sdk
- PilRC 3.2: sourceforge.net/projects/pilrc
- Native Apple Silicon toolchain (not adopted): github.com/User7142/palmdev-macos
- DateFix: github.com/User7142/palm-datefix
- CloudpilotEmu: cloudpilot-emu.github.io
- PalmDB (listings and announcements): palmdb.net
- FuelLog, the worked example for everything above: github.com/lasergirl-ca/palm-fuellog
  (`Makefile`, `fuellog.def`, `src/sections.h`, `src/ptypes.h`, `tools/check-sections.sh`,
  `tools/sectionize.py`, `tools/prcinfo.py`, `tools/mkicons.py`, `toolchain/Dockerfile`,
  `docs/Palm-Clocks-and-DateFix.md`)
