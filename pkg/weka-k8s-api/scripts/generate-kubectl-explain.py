#!/usr/bin/env python3
"""Generate kubectl explain path mapping from Go types and CRD YAML files.

Parses Go source files to map Go type names to their CRD field paths,
then writes docs/_kubectl_explain.json mapping each Go type name to its
kubectl explain path(s) per root CRD.

Output format:
{
    "WekaCluster": {
        "WekaCluster": "wekacluster",
        "WekaClusterSpec": "wekacluster.spec",
        "Network": "wekacluster.spec.network",
        ...
    },
    ...
}
"""

import json
import os
import re

API_DIR = os.path.join("api", "v1alpha1")
DOCS_DIR = "docs"
OUTPUT = os.path.join(DOCS_DIR, "_kubectl_explain.json")

# Root CRD kinds and their spec type names
ROOT_CRDS = {
    "WekaCluster": "WekaClusterSpec",
    "WekaClient": "WekaClientSpec",
    "WekaContainer": "WekaContainerSpec",
    "WekaManualOperation": "WekaManualOperationSpec",
    "WekaPolicy": "WekaPolicySpec",
}


def parse_go_structs():
    """Parse Go source files and return {struct_name: [(json_tag, go_type)]}."""
    structs = {}
    current = None

    for filename in sorted(os.listdir(API_DIR)):
        if not filename.endswith("_types.go"):
            continue
        with open(os.path.join(API_DIR, filename)) as f:
            for line in f:
                m = re.match(r"^type (\w+) struct", line)
                if m:
                    current = m.group(1)
                    structs[current] = []
                    continue
                if current and line.strip() == "}":
                    current = None
                    continue
                if current:
                    m = re.match(
                        r'\s+\w+\s+\*?(\[?\]?\w+)\s+.*json:"(\w+)', line
                    )
                    if m:
                        go_type = m.group(1).lstrip("[]")
                        json_tag = m.group(2)
                        structs[current].append((json_tag, go_type))
    return structs


def build_explain_paths(structs):
    """Build {root_kind: {go_type: explain_path}} for all root CRDs."""
    result = {}

    for kind, spec_type in ROOT_CRDS.items():
        resource = kind.lower()
        paths = {
            kind: resource,
            spec_type: f"{resource}.spec",
        }

        # Also map the status type if it exists
        status_type = kind + "Status"
        if status_type in structs:
            paths[status_type] = f"{resource}.status"

        # BFS from the spec type to discover all reachable sub-types
        queue = [(spec_type, f"{resource}.spec")]
        visited = {spec_type}

        while queue:
            parent_type, parent_path = queue.pop(0)
            if parent_type not in structs:
                continue
            for json_tag, go_type in structs[parent_type]:
                field_path = f"{parent_path}.{json_tag}"
                if go_type in structs and go_type not in visited:
                    # Only map if it's a struct type (not a primitive)
                    if go_type not in paths:
                        paths[go_type] = field_path
                    visited.add(go_type)
                    queue.append((go_type, field_path))

        result[kind] = paths

    return result


def main():
    print("Parsing Go structs...")
    structs = parse_go_structs()
    print(f"  found {len(structs)} struct types")

    print("Building explain paths...")
    result = build_explain_paths(structs)
    for kind, paths in sorted(result.items()):
        print(f"  {kind}: {len(paths)} paths")

    os.makedirs(DOCS_DIR, exist_ok=True)
    with open(OUTPUT, "w") as f:
        json.dump(result, f, indent=2)
    print(f"  wrote {OUTPUT}")


if __name__ == "__main__":
    main()
