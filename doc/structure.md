# Weka Operator Documentation Structure Guidelines

## Purpose
This document provides guidelines for maintaining and extending the Weka Operator documentation structure. It is intended primarily for documentation contributors and maintainers.

## Documentation Philosophy
1. **Single Source of Truth**: Each concept or procedure should be documented in exactly one place
2. **Progressive Disclosure**: Start with high-level concepts before diving into implementation details
3. **Task-Oriented Organization**: Organize content based on user tasks rather than system architecture
4. **Consistent Format**: Maintain consistent formatting and style across all documentation

## Directory Structure

```
./
├── operator/                        # Operator documentation organized by topic
│   ├── clients/                     # Client-related documentation
│   ├── concepts/                    # Fundamental concepts and architecture
│   ├── deployment/                  # Deployment-related documentation
│   ├── networking/                  # Networking documentation
│   └── operations/                  # Operational procedures
├── ai/                              # AI assistant guidelines
├── weka/                            # Weka-specific documentation
│   ├── cli/                         # CLI reference documentation
│   └── drives/                      # Drive-related technical documentation
├── api_dump/                        # API reference documentation (AUTO-GENERATED)
├── missing_data.md                  # Identified gaps in documentation
├── structure.md                     # This file - documentation guidelines
└── summary.xml                       # Reference guide for all documentation
```

## Documentation File Guidelines

### File Naming
- Use kebab-case for all filenames (e.g., `cluster-provisioning.md`)
- Names should be descriptive but concise
- Avoid special characters and spaces
- When referencing filenames in documentation, wrap them in backticks (e.g., `filename.md`) to ensure they're properly formatted when transferred between LLMs or other systems

### File Content Structure
- Start with a clear title using a single H1 (`#`)
- Follow with a brief overview section
- Use H2 (`##`) for main sections
- Use H3 (`###`) for subsections
- Include examples where appropriate
- End with relevant cross-references if applicable

### Cross-References
- Maintain cross-references between related documents
- Use relative paths when linking to other documents
- Update cross-references when reorganizing content

## Directory-Specific Guidelines

### operator/concepts/
- Focus on fundamental architecture and design concepts
- Explain relationships between components
- Define terminology consistently
- Avoid procedure-specific instructions

### operator/deployment/
- Include clear step-by-step procedures
- Provide complete YAML examples
- Explain all relevant configuration options
- Cover initial deployment and configuration tasks

### operator/networking/
- Document all networking options for different environments
- Include diagrams where helpful
- Cover physical, cloud, and hybrid scenarios
- Explain performance implications of different network configurations

### operator/operations/
- Focus on day-2 operations tasks
- Include monitoring, upgrading, scaling, and troubleshooting
- Provide clear command examples
- Document expected output and how to interpret it

### operator/clients/
- Cover client deployment and configuration
- Document CSI driver installation and configuration
- Include workload examples with PVCs and volumes
- Explain performance optimization for client workloads

### ai/
- Maintain guidelines for AI assistants
- Document important conventions and patterns
- Keep critical instructions updated with AIMUST tags
- Ensure AI can properly parse and understand the documentation

### weka/cli/
- Document Weka CLI commands and their usage
- Provide examples with expected output
- Include JSON reference for programmatic usage
- Document command parameters and options

### weka/drives/
- Document drive-related technical details and mechanisms
- Explain drive signing, partitioning, and cluster ID signatures
- Provide drive investigation procedures and troubleshooting
- Include partition type GUIDs and state transitions

## Special Files

### summary.xml
- Serves as the primary reference for all documentation
- Should be updated whenever new documentation is added
- Includes keywords for each document to aid in discovery
- Provides concise summaries of each document's content
- Includes information about api_dump and all other documentation

### missing_data.md
- Documents gaps in current documentation
- Proposes file names and locations for new documentation
- Should be updated as gaps are filled or new gaps identified
- Prioritizes documentation tasks

### api_dump/ Directory
- Contains generated API reference documentation in markdown format
- Generated automatically from code - DO NOT MANUALLY EDIT
- Includes comprehensive API references for all custom resources
- Critical resource for understanding available configuration options
- Includes: WekaCluster, WekaContainer, WekaClient, WekaPolicy, WekaManualOperation

## Maintenance Guidelines

### Adding New Documentation
1. Identify the appropriate directory based on the topic
2. Create the file following naming conventions
3. Update cross-references in related files
4. Add an entry to summary.xml
5. Remove from missing_data.md if the topic was listed there

### Updating Existing Documentation
1. Maintain the same format and structure
2. Update cross-references if content references change
3. Update the entry in summary.xml if the content scope changes

### Reorganizing Documentation
1. Update all cross-references
2. Update the directory structure description in this file
3. Update summary.xml to reflect new locations

## Future Expansion
As the Weka Operator evolves, this documentation structure should expand to include:
1. More detailed examples
2. Environment-specific guides
3. Integration with other Kubernetes components
4. Advanced troubleshooting guides
5. Performance tuning recommendations