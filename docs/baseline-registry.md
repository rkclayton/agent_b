# Baseline registry format

Each release carries the same PC/phone JSON array: `[{"instructions_hash":"<64 lowercase hex>","first_shipped_in":"vMAJOR.MINOR.PATCH"}]`. Hash exact UTF-8 instruction bytes; entries are unique by hash and append-only. A schema match never substitutes for a missing hash. Step 0 adds no registry store or runtime behavior.
