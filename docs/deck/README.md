# Pulse pitch deck

- **[present.html](present.html)**: full-screen, one slide at a time, made for an iPad next to the laptop. Swipe or tap the left/right edge to move; tap the middle for the controls (**Notes** shows the speaker notes, **All** shows every slide). It is one self-contained file.
- **[index.html](index.html)**: all 13 slides on one scrolling page with the notes under each. Print it (Save as PDF, landscape, no margins) for a PDF.
- `slides/*.html`: one file per slide, 1920 × 1080, inline styles only. `deck.json`: order, sections, font.

## On the iPad

With Pulse running, open **https://pulsecrowd.tech/deck** in Safari. For true full screen: Share → **Add to Home Screen**, then open it from the home screen icon. Turn the iPad to landscape. Settings → Display & Brightness → Auto-Lock → Never keeps it on (the page also asks the iPad to stay awake).

No internet at the table: AirDrop `present.html` from the Mac to the iPad and open it from Files. It works offline; only the font falls back to the iPad's own.

Slide content and sources: [../DECK.md](../DECK.md). The spoken script: [../PITCH.md](../PITCH.md). Every number is simulator or synthetic-phone data unless a slide says otherwise; slide 2's figure (159, Itaewon, 29 October 2022) is from Korea Herald and Inquirer/AFP reporting.
