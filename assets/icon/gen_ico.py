from PIL import Image
import os

base = r"d:\ai-work\tdm\assets\icon"
png_dir = os.path.join(base, "png")

sizes = [16, 24, 32, 48, 64, 128, 256]
imgs = []
for s in sizes:
    img = Image.open(os.path.join(png_dir, f"tdm-{s}.png")).convert("RGBA")
    if img.size != (s, s):
        img = img.resize((s, s), Image.LANCZOS)
    imgs.append(img)

ico_path = os.path.join(base, "tdm.ico")
imgs[-1].save(ico_path, format="ICO", sizes=[(s, s) for s in sizes], append_images=imgs[:-1])
print("wrote", ico_path, os.path.getsize(ico_path), "bytes")
