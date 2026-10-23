#!/usr/bin/env bash
# generate.sh — builds the fixture library under fixtures/library/.
# Synthetic media only (ffmpeg testsrc + temporal noise + sine). The
# noise filter matters: plain testsrc compresses so well that files
# land under the 1 MB tiny-file floor and under their nominal bitrate.
# Intermediates (the French subtitle used for muxing) live in /tmp so
# they are never scanned as fixtures themselves.
set -euo pipefail
cd "$(dirname "$0")"
LIB="$PWD/library"
rm -rf "$LIB"
mkdir -p "$LIB/Movies/The Test Movie (2020)" \
         "$LIB/Movies/Control Film (2022)" \
         "$LIB/Movies/Embedded Case (2021)" \
         "$LIB/Movies/Subtitle Cases" \
         "$LIB/Movies/Health Cases" \
         "$LIB/Movies/Empty Movie (2023)" \
         "$LIB/Shows/Test Show/Season 01"

NOISE=(-vf "noise=alls=50:allf=t")
A_OUT=(-c:a aac -b:a 96k -shortest)

# --- Duplicate movie pair: same title+year, different codec/res/size ---
ffmpeg -v error -y -f lavfi -i testsrc=size=1920x1080:rate=24:duration=10 -f lavfi -i sine=frequency=440:duration=10 \
  "${NOISE[@]}" -c:v libx264 -b:v 4M "${A_OUT[@]}" "$LIB/Movies/The Test Movie (2020)/The Test Movie (2020) 1080p BluRay x264-GROUP.mkv"
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=10 -f lavfi -i sine=frequency=440:duration=10 \
  "${NOISE[@]}" -c:v libx265 -b:v 2M "${A_OUT[@]}" "$LIB/Movies/The Test Movie (2020)/The.Test.Movie.2020.720p.WEB-DL.x265-GRP.mp4"
# --- Different edition of the same movie (must NOT merge) ---
ffmpeg -v error -y -f lavfi -i testsrc=size=1920x1080:rate=24:duration=10 -f lavfi -i sine=frequency=440:duration=10 \
  "${NOISE[@]}" -c:v libx264 -b:v 4M "${A_OUT[@]}" "$LIB/Movies/The Test Movie (2020)/The Test Movie (2020) Extended 1080p BluRay x264.mkv"
# --- Healthy control movie (no findings expected) ---
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=8 -f lavfi -i sine=frequency=330:duration=8 \
  "${NOISE[@]}" -c:v libx264 -b:v 2500k "${A_OUT[@]}" "$LIB/Movies/Control Film (2022)/Control Film (2022).mkv"
# --- TV: S01E01 in two encodes + a different episode ---
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=8 -f lavfi -i sine=frequency=440:duration=8 \
  "${NOISE[@]}" -c:v libx264 -b:v 2500k "${A_OUT[@]}" "$LIB/Shows/Test Show/Season 01/Test Show S01E01 720p WEB-DL x264.mkv"
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=8 -f lavfi -i sine=frequency=440:duration=8 \
  "${NOISE[@]}" -c:v libx265 -b:v 1500k "${A_OUT[@]}" "$LIB/Shows/Test Show/Season 01/Test.Show.S01E01.720p.WEB-DL.x265.mp4"
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=8 -f lavfi -i sine=frequency=440:duration=8 \
  "${NOISE[@]}" -c:v libx264 -b:v 2500k "${A_OUT[@]}" "$LIB/Shows/Test Show/Season 01/Test Show S01E02 720p WEB-DL x264.mkv"

# --- Subtitle texts (python3 for exact cue timing) ---
python3 - <<'PYEOF'
import os
lib = os.path.join(os.getcwd(), "library")
def ts(sec):
    return f"00:00:{int(sec):02d},000"
def srt(path, sentences, reps=3, step=5):
    cues, t = [], 0.0
    for i, s in enumerate(sentences * reps):
        cues.append(f"{i+1}\n{ts(t)} --> {ts(t+step-1)}\n{s}\n")
        t += step
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(cues))
es = ["No sé qué estás diciendo en este momento.",
      "Tú me dijiste que estarías aquí cuando te necesitara.",
      "Ahora me estás diciendo que esto no es tu problema.",
      "Tenemos que irnos ahora porque esto es muy peligroso para todos nosotros.",
      "No hay manera de que nos quedemos aquí más tiempo del que tenemos."]
en = ["I do not know what you are saying right now.",
      "You told me that you would be here when I needed you.",
      "Now you are telling me that this is not your problem.",
      "We have to leave now because this is very dangerous for all of us.",
      "There is no way that we can stay here any longer than we have to."]
fr = ["Je ne sais pas de quoi tu parles en ce moment.",
      "Tu as dit que tu serais là quand je aurais besoin de toi.",
      "Maintenant tu me dis que ce n est pas ton problème.",
      "Nous devons partir maintenant parce que c est très dangereux pour nous tous.",
      "Il n y a pas moyen que nous restions ici plus longtemps que nous le devons."]
sub = os.path.join(lib, "Movies", "Subtitle Cases")
srt(os.path.join(sub, "Spanish Mislabeled (2021).en.srt"), es)   # declared en, actually es => FLAG
srt(os.path.join(sub, "Spanish Correct (2021).es.srt"), es)      # control, no flag
srt(os.path.join(sub, "English Correct (2021).en.srt"), en)      # control, no flag
srt("/tmp/french-source.srt", fr, reps=3, step=2)                # 15 cues inside 30s; muxing source only
open(os.path.join(lib, "Movies", "Health Cases", "Empty Subtitle.en.srt"), "w").close()
PYEOF

# --- Embedded French subtitle tagged eng (must FLAG as mismatch) ---
ffmpeg -v error -y -f lavfi -i testsrc=size=640x360:rate=24:duration=30 -f lavfi -i sine=frequency=440:duration=30 -i /tmp/french-source.srt \
  -map 0:v -map 1:a -map 2:s -vf "noise=alls=35:allf=t" -c:v libx264 -b:v 700k -c:a aac -b:a 96k -c:s copy \
  -metadata:s:s:0 language=eng "$LIB/Movies/Embedded Case (2021)/Embedded Case (2021).mkv"

# --- Health fixtures ---
: > "$LIB/Movies/Health Cases/Broken Empty (2022).mkv"                       # 0-byte
ffmpeg -v error -y -f lavfi -i testsrc=size=1280x720:rate=24:duration=8 -f lavfi -i sine=frequency=440:duration=8 \
  "${NOISE[@]}" -c:v libx264 -b:v 2500k "${A_OUT[@]}" /tmp/trunc-source.mp4
head -c 400000 /tmp/trunc-source.mp4 > "$LIB/Movies/Health Cases/Truncated Film (2022).mp4"   # moov cut off
echo "this is plain text, not a media file at all" > "$LIB/Movies/Health Cases/Fake Film (2022).mkv"
head -c 200000 /tmp/trunc-source.mp4 > "$LIB/Movies/Health Cases/Incomplete Film (2022).mkv.part"
# "Empty Movie (2023)" folder intentionally left empty.

echo "Fixture library generated at $LIB"
find "$LIB" -type f -printf "%s\t%p\n" | sort -n
du -sh "$LIB"
