Option Explicit
Dim shell, command, index, value
Set shell = CreateObject("WScript.Shell")
command = ""
For index = 0 To WScript.Arguments.Count - 1
  value = Replace(WScript.Arguments(index), """", "\""")
  If Len(command) > 0 Then command = command & " "
  command = command & """" & value & """"
Next
If Len(command) = 0 Then WScript.Quit 2
shell.Run command, 0, False
