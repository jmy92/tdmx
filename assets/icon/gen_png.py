import cairosvg, os

base = r"d:\ai-work\tdm\assets\icon"
svg = os.path.join(base, "app-icon.svg")
png_dir = os.path.join(base, "png")
os.makedirs(png_dir, exist_ok=True)

sizes = [16, 24, 32, 48, 64, 128, 256, 512]
for s in sizes:
    out = os.path.join(png_dir, f"tdm-{s}.png")
    cairosvg.svg2png(url=svg, write_to=out, output_width=s, output_height=s)
    print("wrote", out)

# 1024 for store/marketing
out = os.path.join(base, "tdm-1024.png")
cairosvg.svg2png(url=svg, write_to=out, output_width=1024, output_height=1024)
print("wrote", out)
