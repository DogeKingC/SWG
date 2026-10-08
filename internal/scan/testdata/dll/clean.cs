using System.Collections.Generic;
public class Gun { public List<int> Ammo = new List<int>(); public int Count() { return System.Math.Max(Ammo.Count, 0); } }
