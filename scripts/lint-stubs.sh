#!/bin/sh
set -eu
paths="go kernel"
if [ -d mockllm ]; then
  paths="$paths mockllm"
fi
if rg -n 'TODO|FIXME|XXX|NotImplemented|unimplemented|raise NotImplementedError' $paths \
  --glob '!**/testdata/**' \
  --glob '!**/.venv/**' \
  --glob '!**/docs/**'; then
  exit 1
fi
exit 0
