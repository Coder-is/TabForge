import sys
from tabforge_data import DataBundle

sys.stdout.reconfigure(encoding="utf-8")
bundle = DataBundle.open(sys.argv[1] if len(sys.argv) > 1 else "examples/complete/Generated")
tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables")
item = tables["items"][0]
print("%s: ownerId=%s" % (item["name"], item["ownerId"]))
