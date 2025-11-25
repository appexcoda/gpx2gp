# gpx2gp

Simple Guitar Pro GPX to GP file converter.

Example:

``` bash
readarray -t files <<<"$(ls *.gpx)"
for file in "${files[@]}"; do ./gpx2gp.exe -f "$file" -o "${file%%.*}"; done
```

## Acknowledgments

Based on file format information from [rust-gpx-reader](https://github.com/Antti/rust-gpx-reader) and [alphaTab](https://github.com/CoderLine/alphaTab ).

## License

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.