Validation crawler
==================
Validate links on your static website!

Built with [Go](http://golang.org/) 1.6.

How to run
----------
- Provide a set of seed URLs, each ending with a slash:

  ```
  $ go run crawler.go https://github.com/twitter/scalding/wiki/
  ```

TODO
----
- Print statistics
- Set proper user-agent
- Group reports per page

License
-------
- [MIT License](http://opensource.org/licenses/mit-license.php)
