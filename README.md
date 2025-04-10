# Core Infra
This repository contains modules providing the core infra for other projects
with many required functionalities, some of which are high lighted as follows

### Errors package
Errors package available out of the box in golang does not provide options to
carry information regarding error codes, this typically becomes handy while
comparing and checking for error types, rather than comparing error message
strings returned by different functions, enabling writing more sturdy, reliable
and quality code
Includes most common supported error types like
- Already Exists / duplicate entry
- Entry Not Found
- Invalid Argument
- and more, while defaulting to unknown error type, while code is not set

### Mongo DB Client
Mongo DB client routines, allowing streamlining and centralizing the core
functions and operations with standard implementation. with most of the
capabilities offered by mongo DB
